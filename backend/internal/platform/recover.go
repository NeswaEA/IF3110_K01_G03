package platform

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recover turns a handler panic into one JSON log line and, when nothing was sent yet, a JSON 500.
// The panic value is never logged: it may hold request data. Its type and the stack locate the bug.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(v) // net/http's own signal to drop the connection quietly
			}
			slog.ErrorContext(r.Context(), "HTTP handler panicked", "event", "http.panic", "panic_type", fmt.Sprintf("%T", v), "stack", string(debug.Stack()))
			if recorder.wroteHeader {
				panic(http.ErrAbortHandler) // the response already started; only closing the connection is honest
			}
			_ = WriteError(recorder, r, errors.New("handler panic"))
		}()
		next.ServeHTTP(recorder, r)
	})
}
