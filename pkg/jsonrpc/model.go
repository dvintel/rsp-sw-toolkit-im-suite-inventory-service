package jsonrpc

import (
	"encoding/json"
	"errors"
)

const (
	RpcVersion = "2.0"
)

var (
	//ErrInvalidVersion error returned when JsonRpc version is not 2.0
	ErrInvalidVersion = errors.New("invalid jsonrpc version")
	//ErrMissingMethod error returned when method field is missing or empty
	ErrMissingMethod = errors.New("missing or empty method field")
	//ErrMissingId error returned when id field is missing or empty
	ErrMissingId = errors.New("missing or empty id field")
)

type Message interface {
	Validate() error
}

type Notification struct {
	Version string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type Request struct {
	Notification        // embed
	Id           string `json:"id"`
}

func (js *Notification) Validate() error {
	if js.Version != RpcVersion {
		return ErrInvalidVersion
	}

	if js.Method == "" {
		return ErrMissingMethod
	}

	return nil
}

func (js *Request) Validate() error {
	if js.Id == "" {
		return ErrMissingId
	}

	return js.Notification.Validate()
}
