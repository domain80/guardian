package guardian

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

/******
*
* SESSION RELATED CODE
*
******/
type Session struct {
	id          string
	Data        map[string]interface{} // does not map to a strict type because of user preferences
	idleTime    time.Time
	lifetime    time.Time
	renewalTime time.Time
	Cookie      http.Cookie
}

/******
*
* SESSION MANAGER RELATED CODE
*
******/
type contextKey string
type namespaceManagerInstance map[string]struct{}
type namespaceManager struct {
	_instance namespaceManagerInstance
	_lock     sync.Mutex
}

func (m *namespaceManager) getInstance() namespaceManagerInstance {
	if m._instance == nil {
		m._lock.Lock()
		defer m._lock.Unlock()
		if m._instance == nil {
			m._instance = namespaceManagerInstance{}
			return m._instance
		} else {
			return m._instance
		}
	}

	return m._instance
}

func newNameSpaceManager() namespaceManager {
	return namespaceManager{
		_instance: namespaceManagerInstance{},
		_lock:     sync.Mutex{},
	}
}

var managerIDs = newNameSpaceManager()

type sessionManager struct {
	Store          Store
	id             string
	contextKey     contextKey    // use an md5 hash of the id coupled with the creation time as the context key
	infologger     log.Logger    // set as private; public use will come later
	errLogger      log.Logger    // set as private; public use will come later
	IdleTimeout    time.Duration //
	Lifetime       time.Duration
	RenewalTimeout time.Duration
}

type SessionManagerConstructorParams struct {
	Store          Store
	Infologger     log.Logger    // set as private; public use will come later
	ErrLogger      log.Logger    // set as private; public use will come later
	IdleTimeout    time.Duration //
	Lifetime       time.Duration
	RenewalTimeout time.Duration
	ContextKey     contextKey
}

// TODO: Find a way to pass optional second argument to the New function
// this second parameter will be of type SessionManagerParams and will define
// other parameters of a session manager at creation time
func New(namespace string) (sessionManager, error) {
	id := ""
	if _, ok := managerIDs.getInstance()[namespace]; ok {
		return sessionManager{}, errors.New("err: session namespace already exists")
	} else {
		id = namespace
		managerIDs.getInstance()[namespace] = struct{}{}
	}

	hashValue := fmt.Sprintf("%s+%d", id, time.Now().UnixNano())
	binaryCtx := md5.Sum([]byte(hashValue))
	key := contextKey(hex.EncodeToString(binaryCtx[:]))

	return sessionManager{
		Store:          Store{},
		id:             id,
		contextKey:     key,
		infologger:     *log.New(os.Stdout, "SessionInfo:\t", log.LUTC),
		errLogger:      *log.New(os.Stdout, "SessionErr:\t", log.LUTC),
		IdleTimeout:    15 * time.Minute,
		Lifetime:       2 * time.Hour,
		RenewalTimeout: time.Minute,
	}, nil
}

/* **
// TODO: implement the following funcs:
	createSession() Ssession
	watchTimeouts()
	populateContext(ctx context.Context)
		NOTE: The context population can be done in this manner:
		-	if there already exists a context key, in the given ctx
			then add the decoded data to the context
		- 	else if there is no context key available in the given ctx
			create a new context.
		-	This ensures that in the case where there are different context managers
			used in the same application, data from each context manager will be made
			available while avoiding the issues of duplication and confusion of which data
			values are more important as any overriden data by the second session manager
			will be considered more important by default.
		- 	In view of this, it should be noted that for multiple session managers,
			the order in which session middlewares are applied defines the order of priority
			and security levels.
		NOTE: Method 2: context keys as seen by alex edwards
		-	in the case where there are more than one session managers in use
			for the same applicatin, the use of unique randomly generated context keys
			to populate the context may be amore ideal.
		- 	The context keys will be generated using the sessionManager id which is already
			checked against duplication

	renewSession(session *Session)
	invalidateSession(session *Session)		// set session cookie to a time in the past

	managerMiddleware ~ manage(next http.Handler) http.HandleFunc
		loggerMiddleware(next http.Handler) http.HandleFunc
** */

// generate a new session id using the session managers ID and the
// timestamp at which the function was called
func (s *sessionManager) newSessionID() string {
	hashValue := fmt.Sprintf("%s+%d", string(s.id), time.Now().UnixNano())
	binaryCtx := md5.Sum([]byte(hashValue))
	return (hex.EncodeToString(binaryCtx[:]))
}

// create a new session in which data can be stored,
// the session created is automatically saved in the sessionManager store
func (s *sessionManager) CreateSession() Session {
	id := s.newSessionID()
	return Session{
		id:          id,
		Data:        map[string]interface{}{},
		idleTime:    time.Now().Add(s.IdleTimeout),
		lifetime:    time.Now().Add(s.Lifetime),
		renewalTime: time.Now().Add(s.RenewalTimeout),
		Cookie: http.Cookie{
			Name:     fmt.Sprintf("session_%s", s.id),
			Value:    id,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
			HttpOnly: true,
			Expires:  time.Now().Add(s.Lifetime),
		},
	}
}

// watch timeouts in the order: renewalTime, idleTime and lifeTime;
// the sessionID is renewed whenever the renewal time is up and the idleTime or lifetime have not elapsed yet
// for every request, the idletime if not elapsed yet is reset; The idle time however is reset only if it has elapsed
// the after the idletime and lifetimes have elapsed, the session is invalidated and the session cookie is removed.
func (s *sessionManager) watchTimeouts(session *Session) {
	if time.Now().After(session.idleTime) || time.Now().After(session.lifetime) {
		s.invalidateSession(session)
		return
	} else if time.Now().After(session.renewalTime) {
		s.renewSession(session)
		session.idleTime = time.Now().Add(s.IdleTimeout)
	}
}

// delete session from store and set cookie to a time value in the past
// set session cookie to a time in the past
func (s *sessionManager) invalidateSession(session *Session) {
	if err := s.Store.Delete(session.id); err != nil {
		s.errLogger.Println(err.Error())
	}
	session.Cookie.Name = ""
	session.Cookie.Value = ""
	session.Cookie.Expires = time.Now().Add(-s.IdleTimeout)
}

// set a new session id for both the session and the session cookie
// and ensure that the store is also updated with the changes
func (s *sessionManager) renewSession(session *Session) {
	newId := s.newSessionID()
	oldId := session.id

	session.id = newId
	session.Cookie.Value = newId

	s.Store.Update(oldId, session)
}