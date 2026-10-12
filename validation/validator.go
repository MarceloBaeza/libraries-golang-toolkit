package validation

import "github.com/go-playground/validator/v10"

var validate = validator.New()

type ValidationResult struct {
	Success bool
	Errors  map[string]string
}

type ValidationRule func(v *validator.Validate, s any) *FieldError

type FieldError struct {
	Field   string
	Message string
}

func RunValidations(s any, rules []ValidationRule) *ValidationResult {
	errs := make(map[string]string)
	for _, rule := range rules {
		if fe := rule(validate, s); fe != nil {
			errs[fe.Field] = fe.Message
		}
	}
	return &ValidationResult{Success: len(errs) == 0, Errors: errs}
}
