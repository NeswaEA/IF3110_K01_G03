package contracts

type DomainError struct {
	Code    string
	Message string
	Fields  map[string]string
	// Status overrides the HTTP status of Code when a well-formed request is rejected by the domain.
	Status int   `json:"-"`
	Cause  error `json:"-"`
}

func (e *DomainError) Error() string { return e.Message }

func (e *DomainError) Unwrap() error { return e.Cause }
