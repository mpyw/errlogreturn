package sinkflag

import "errors"

func do() error { return errors.New("boom") }

type Client struct{}

func (c *Client) Send(v any) {}

func Report(v any) {}

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
