package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// APIRequest can be carried unchanged over a relay or a direct connection.
type APIRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// DispatchAPI decodes a client API request and calls the same API used locally.
func DispatchAPI(ctx context.Context, api API, data json.RawMessage) (json.RawMessage, error) {
	var request APIRequest
	if err := decode(data, &request); err != nil {
		return nil, err
	}
	var result any
	var err error
	switch request.Method {
	case "execute":
		var a ExecuteRequest
		if err = decode(request.Params, &a); err == nil {
			result, err = api.Execute(ctx, a)
		}
	case "read":
		var a ReadRequest
		if err = decode(request.Params, &a); err == nil {
			result, err = api.Read(ctx, a)
		}
	case "list":
		var a struct{}
		if err = decode(request.Params, &a); err == nil {
			result, err = api.List(ctx)
		}
	case "control":
		var a ControlRequest
		if err = decode(request.Params, &a); err == nil {
			result, err = api.Control(ctx, a)
		}
	default:
		err = fmt.Errorf("unknown client API method %q", request.Method)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}
func decode(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("invalid trailing request data")
	}
	return nil
}
