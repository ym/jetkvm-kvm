package kvm

import (
	"fmt"
	"log"
	"os"
	"reflect"
	"sync"
	"unsafe"

	"github.com/pion/logging"
)

// TODO: support multiple sessions?
var (
	currentSession *Session
	sessionMutex   sync.RWMutex

	sessionLogger *log.Logger
)

type SessionStateSetter func(*Session) *Session

// we really should not do this, but due to the way pion logging works
// we need to patch the logger for the session, so that we can get the correct line number in the caller.
func init() {
	logLevel := defaultLoggerFactory.ScopeLevels["session"]
	if logLevel < logging.LogLevelTrace {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			logger.Errorf("unable to get session logger: %v", r)
		}
	}()

	// i need to get the trace field
	ptr := reflect.ValueOf(logger)
	tracePrivateField := ptr.Elem().FieldByName("trace")

	traceField := reflect.NewAt(tracePrivateField.Type(), unsafe.Pointer(tracePrivateField.UnsafeAddr())).Elem()

	sessionLogger = traceField.Interface().(*log.Logger)
}

func sessionLoggerTracef(format string, args ...interface{}) {
	if sessionLogger == nil {
		return
	}

	// if arg is session, replace with sessionToText
	newArgs := make([]interface{}, len(args))
	for i, arg := range args {
		if s, ok := arg.(*Session); ok {
			newArgs[i] = sessionToText(s)
		} else {
			newArgs[i] = arg
		}
	}

	callDepth := 3 // this frame + wrapper func + caller
	msg := fmt.Sprintf(format, newArgs...)
	if err := sessionLogger.Output(callDepth, msg); err != nil {
		fmt.Fprintf(os.Stderr, "Unable to log: %s", err)
	}
}

func sessionToText(session *Session) string {
	if session == nil {
		return "nil"
	}
	if session.peerConnection == nil {
		return "PeerConnection-nil"
	}

	// well, let's use reflect to get private field `statsID` again :-(
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "Unable to get statsID: %v", r)
		}
	}()
	ptr := reflect.ValueOf(session.peerConnection)
	statsIDField := ptr.Elem().FieldByName("statsID")
	statsID := reflect.NewAt(statsIDField.Type(), unsafe.Pointer(statsIDField.UnsafeAddr())).Elem()

	return statsID.String()
}

func getCurrentSession() *Session { //nolint:unused
	sessionLoggerTracef("getCurrentSession was called, currentSession: %v", currentSession)

	sessionMutex.RLock()
	defer sessionMutex.RUnlock()

	return currentSession
}

func setCurrentSession(session *Session) { //nolint:unused
	sessionLoggerTracef("setCurrentSession was called, currentSession: %v, session: %v", currentSession, session)
	sessionMutex.Lock()
	defer sessionMutex.Unlock()

	currentSession = session
}

func clearCurrentSession() { //nolint:unused
	sessionLoggerTracef("clearCurrentSession was called, currentSession: %v", currentSession)

	sessionMutex.Lock()
	defer sessionMutex.Unlock()

	currentSession = nil
}

func setCurrentSessionWithSetter(setter SessionStateSetter) {
	sessionLoggerTracef("setCurrentSessionWithSetter was called, currentSession: %v", currentSession)

	sessionMutex.Lock()
	defer sessionMutex.Unlock()

	newSession := setter(currentSession)
	currentSession = newSession

	sessionLoggerTracef("setCurrentSessionWithSetter completed, currentSession: %v, newSession: %v", currentSession, newSession)
}
