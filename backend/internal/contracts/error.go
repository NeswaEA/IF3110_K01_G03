package contracts

type DomainError struct {
	Code    string
	Message string
	Fields  map[string]string
	RunID   *string
	Cause   error `json:"-"`
}

func (e *DomainError) Error() string { return e.Message }

func (e *DomainError) Unwrap() error { return e.Cause }
