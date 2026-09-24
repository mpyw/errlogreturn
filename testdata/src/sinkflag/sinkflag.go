package sinkflag

import "errors"

func do() error { return errors.New("boom") }

type Client struct{}

func (c *Client) Send(v any) {}

func Report(v any) {}

type Gen[T any] struct{}

func (Gen[T]) Emit(v any) {}

type Value struct{}

func (Value) Push(v any) {}

// ===== SHOULD REPORT =====

func function() error {
	err := do()
	Report(err) // want `error is logged here`
	return err
}

func method(c *Client) error {
	err := do()
	c.Send(err) // want `error is logged here`
	return err
}

// The flag spells this method with a *, which a value receiver does not have.
func valueReceiver(v Value) error {
	err := do()
	v.Push(err) // want `error is logged here`
	return err
}

// A method of a generic type is named with its type parameters.
func genericReceiver(g Gen[int]) error {
	err := do()
	g.Emit(err) // want `error is logged here`
	return err
}
