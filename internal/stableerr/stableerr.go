package stableerr

import "fmt"

type messageError string

func (e messageError) Error() string {
	return string(e)
}

func New(message string) error {
	return messageError(message)
}

func Errorf(format string, args ...any) error {
	return messageError(fmt.Sprintf(format, args...))
}
