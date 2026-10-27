package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"openwrt-wifi-guard/internal/rpc"
	"os"
	"time"
)

// uHTTPd injects transport session metadata into every authorized ubus call.
// Native rpcd already enforced its ACL; never forward or expose that session.
func pluginParams(data []byte) (json.RawMessage, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return nil, errors.New("invalid RPC input")
	}
	delete(fields, "ubus_rpc_session")
	return json.Marshal(fields)
}
func runRPC(args []string) error {
	fs := flag.NewFlagSet("rpc", flag.ContinueOnError)
	socket := fs.String("socket", rpc.DefaultSocket, "Private daemon socket")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 1 && fs.Arg(0) == "list" {
		return json.NewEncoder(os.Stdout).Encode(rpc.Methods())
	}
	if fs.NArg() != 2 || fs.Arg(0) != "call" {
		return errors.New("usage: wifi-guard rpc [-socket path] list | call METHOD")
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, rpc.MaxRequest+1))
	if err != nil || len(data) > rpc.MaxRequest {
		return errors.New("invalid RPC input")
	}
	params, err := pluginParams(data)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 34*time.Second)
	defer cancel()
	result, err := rpc.Call(ctx, *socket, fs.Arg(1), params)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(result))
	return err
}
