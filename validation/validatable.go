package validation

import "github.com/go-playground/validator/v10"

type Validatable interface {
	Validate(v *validator.Validate) *FieldError
}
