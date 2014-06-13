package boltstore

import (
	"bytes"
	"encoding/base32"
	"encoding/gob"
	"net/http"
	"strings"
	"time"
	"code.google.com/p/gogoprotobuf/proto"

	"github.com/boltdb/bolt"
	"github.com/gorilla/securecookie"
	"github.com/gorilla/sessions"
)

// store represents a session store.
type store struct {
	codecs []securecookie.Codec
	config Config
	db     *bolt.DB
}

// Get returns a session for the given name after adding it to the registry.
//
// See gorilla/sessions FilesystemStore.Get().
func (s *store) Get(r *http.Request, name string) (*sessions.Session, error) {
	return sessions.GetRegistry(r).Get(s, name)
}

// New returns a session for the given name without adding it to the registry.
//
// See gorilla/sessions FilesystemStore.New().
func (s *store) New(r *http.Request, name string) (*sessions.Session, error) {
	var err error
	session := sessions.NewSession(s, name)
	session.Options = &s.config.SessionOptions
	session.IsNew = true
	if c, errCookie := r.Cookie(name); errCookie == nil {
		err = securecookie.DecodeMulti(name, c.Value, &session.ID, s.codecs...)
		if err == nil {
			ok, err := s.load(session)
			session.IsNew = !(err == nil && ok) // not new if no error and data available
		}
	}
	return session, err
}

// Save adds a single session to the response.
func (s *store) Save(r *http.Request, w http.ResponseWriter, session *sessions.Session) error {
	if session.Options.MaxAge < 0 {
		s.delete(session)
		http.SetCookie(w, sessions.NewCookie(session.Name(), "", session.Options))
	} else {
		// Build an alphanumeric ID.
		if session.ID == "" {
			session.ID = strings.TrimRight(base32.StdEncoding.EncodeToString(securecookie.GenerateRandomKey(32)), "=")
		}
		if err := s.save(session); err != nil {
			return err
		}
		encoded, err := securecookie.EncodeMulti(session.Name(), session.ID, s.codecs...)
		if err != nil {
			return err
		}
		http.SetCookie(w, sessions.NewCookie(session.Name(), encoded, session.Options))
	}
	return nil
}

// Close closes the database.
func (s *store) Close() error {
	return s.db.Close()
}

// open Opens a database and sets it to the session store.
func (s *store) open() error {
	// Open a database.
	db, err := bolt.Open(s.config.DBOptions.Path, 0666)
	if err != nil {
		return err
	}
	// Create a bucket if it does not exist.
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(s.config.DBOptions.BucketName)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.db = db
	return nil
}

// load loads a session data from the database.
// True is returned if there is a session data in the database.
func (s *store) load(session *sessions.Session) (bool, error) {
	// exists represents whether a session data exists or not.
	var exists bool
	err := s.db.Update(func(tx *bolt.Tx) error {
		id := []byte(session.ID)
		bucket := tx.Bucket(s.config.DBOptions.BucketName)
		// Get the session data.
		data := bucket.Get(id)
		if data == nil {
			return nil
		}
		sessionData := &Session{}
		// Convert the byte slice to the Session struct value.
		if err := proto.Unmarshal(data, sessionData); err != nil {
			return err
		}
		// Check the expiration of the session data.
		if *sessionData.ExpiresAt > 0 && *sessionData.ExpiresAt < time.Now().Unix() {
			if err := bucket.Delete(id); err != nil {
				return err
			}
			return nil
		}
		exists = true
		dec := gob.NewDecoder(bytes.NewBuffer(sessionData.Values))
		return dec.Decode(&session.Values)
	})
	return exists, err
}

// delete removes the key-value from the database.
func (s *store) delete(session *sessions.Session) error {
	err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(s.config.DBOptions.BucketName).Delete([]byte(session.ID))
	})
	if err != nil {
		return err
	}
	return nil
}

// save stores the session data in the database.
func (s *store) save(session *sessions.Session) error {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(session.Values)
	if err != nil {
		return err
	}
	data, err := proto.Marshal(NewSession(buf.Bytes(), session.Options.MaxAge))
	if err != nil {
		return nil
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(s.config.DBOptions.BucketName).Put([]byte(session.ID), data)
	})
	return err
}

// New creates and returns a session store.
func New(config Config, keyPairs ...[]byte) (*store, error) {
	config.setDefault()
	store := &store{
		codecs: securecookie.CodecsFromPairs(keyPairs...),
		config: config,
	}
	if err := store.open(); err != nil {
		return nil, err
	}
	return store, nil
}