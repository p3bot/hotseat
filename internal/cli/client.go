// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

const clientResultText = `Print one JSON object and exit. outcome is ok, timeout, closed, refused, unavailable, or connection_failure.`

func addClientCommands(root *cobra.Command) {
	root.AddCommand(
		newCreateCmd(),
		newPublishCmd(),
		newReadCmd(),
		newWaitCmd(),
		newCloseCmd(),
		newListCmd(),
	)
}

func newCreateCmd() *cobra.Command {
	var address, name string
	cmd := newClientCmd("create", "Create a conversation", `  hotseat create --name job
  hotseat create --address 127.0.0.1:4727 --name job`, func(cmd *cobra.Command) error {
		return call(cmd, address, "create", client.CreateRequest{Name: name})
	})
	addAddress(cmd, &address)
	cmd.Flags().StringVar(&name, "name", "", "conversation name")
	markRequired(cmd, "name")
	return cmd
}

func newPublishCmd() *cobra.Command {
	var address, conv, from, body, key string
	var to []string
	var bodyFile string
	cmd := newClientCmd("publish", "Publish a message", `  hotseat publish --conversation job --from alice --to bob --body hello --idempotency-key 1
  hotseat publish --conversation job --from alice --to bob --body-file note.txt --idempotency-key 1`, func(cmd *cobra.Command) error {
		text, err := publishBody(cmd, body, bodyFile)
		if err != nil {
			return err
		}
		return call(cmd, address, "publish", client.PublishRequest{
			Conversation:   conv,
			From:           from,
			To:             to,
			Body:           text,
			IdempotencyKey: key,
		})
	})
	addAddress(cmd, &address)
	cmd.Flags().StringVar(&conv, "conversation", "", "conversation name")
	cmd.Flags().StringVar(&from, "from", "", "sender name")
	cmd.Flags().StringArrayVar(&to, "to", nil, "addressee; repeat for more than one; omit for none")
	cmd.Flags().StringVar(&body, "body", "", "message body")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "file to read the body from; - reads stdin")
	cmd.Flags().StringVar(&key, "idempotency-key", "", "caller-supplied idempotency key")
	markRequired(cmd, "conversation", "from", "idempotency-key")
	return cmd
}

func newReadCmd() *cobra.Command {
	var address, conv, name string
	var cursor, limit int64
	cmd := newClientCmd("read", "Read messages after a cursor", `  hotseat read --conversation job --cursor 0 --limit 50
  hotseat read --conversation job --cursor 0 --limit 50 --name bob`, func(cmd *cobra.Command) error {
		return call(cmd, address, "read", client.ReadRequest{
			Conversation: conv,
			Cursor:       cursor,
			Limit:        limit,
			Name:         changedString(cmd, "name", name),
		})
	})
	addAddress(cmd, &address)
	cmd.Flags().StringVar(&conv, "conversation", "", "conversation name")
	cmd.Flags().Int64Var(&cursor, "cursor", 0, "last finished sequence; 0 has seen nothing")
	cmd.Flags().Int64Var(&limit, "limit", 0, "maximum number of messages")
	cmd.Flags().StringVar(&name, "name", "", "participant name; omit to read every message")
	markRequired(cmd, "conversation", "cursor", "limit")
	return cmd
}

func newWaitCmd() *cobra.Command {
	var address, conv, name, deadline string
	var cursor int64
	cmd := newClientCmd("wait", "Wait for a matching message", `  hotseat wait --conversation job --cursor 0 --name bob --deadline 30s`, func(cmd *cobra.Command) error {
		return call(cmd, address, "wait", client.WaitRequest{
			Conversation: conv,
			Cursor:       cursor,
			Name:         changedString(cmd, "name", name),
			Deadline:     changedString(cmd, "deadline", deadline),
		})
	})
	addAddress(cmd, &address)
	cmd.Flags().StringVar(&conv, "conversation", "", "conversation name")
	cmd.Flags().Int64Var(&cursor, "cursor", 0, "last finished sequence; 0 has seen nothing")
	cmd.Flags().StringVar(&name, "name", "", "participant name; omit to wait for the next message")
	cmd.Flags().StringVar(&deadline, "deadline", "", "wait limit, such as 30s or 500ms; omit to wait without one")
	markRequired(cmd, "conversation", "cursor")
	return cmd
}

func newCloseCmd() *cobra.Command {
	var address, conv string
	cmd := newClientCmd("close", "Close a conversation", `  hotseat close --conversation job`, func(cmd *cobra.Command) error {
		return call(cmd, address, "close", client.CloseRequest{Conversation: conv})
	})
	addAddress(cmd, &address)
	cmd.Flags().StringVar(&conv, "conversation", "", "conversation name")
	markRequired(cmd, "conversation")
	return cmd
}

func newListCmd() *cobra.Command {
	var address string
	cmd := newClientCmd("list", "List conversations", `  hotseat list`, func(cmd *cobra.Command) error {
		return call(cmd, address, "list", struct{}{})
	})
	addAddress(cmd, &address)
	return cmd
}

func newClientCmd(use, short, example string, run func(cmd *cobra.Command) error) *cobra.Command {
	return &cobra.Command{
		Use:           use,
		Short:         short,
		Long:          clientResultText,
		Example:       example,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd)
		},
	}
}

func addAddress(cmd *cobra.Command, address *string) {
	cmd.Flags().StringVar(address, "address", bus.DefaultListen, "bus address")
}

func markRequired(cmd *cobra.Command, names ...string) {
	for _, name := range names {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(err)
		}
	}
}

// changedString reports the flag value when the caller passed it, including
// an empty string. A flag the caller left unset stays out of the request.
func changedString(cmd *cobra.Command, flag, value string) *string {
	if !cmd.Flags().Changed(flag) {
		return nil
	}
	return &value
}

// publishBody takes exactly one source. A file is how a body outgrows one
// command argument; "-" reads stdin so the client still writes nothing.
func publishBody(cmd *cobra.Command, inline, path string) (string, error) {
	inlineSet := cmd.Flags().Changed("body")
	fileSet := cmd.Flags().Changed("body-file")
	switch {
	case inlineSet && fileSet:
		return "", errors.New("pass only one of --body and --body-file")
	case !inlineSet && !fileSet:
		return "", errors.New("a body is required; pass --body or --body-file")
	case inlineSet:
		return inline, nil
	}
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(cmd.InOrStdin())
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func call(cmd *cobra.Command, address, op string, body any) error {
	res, err := client.Do(cmd.Context(), address, op, body)
	if err != nil {
		return err
	}
	return writeResult(cmd.OutOrStdout(), res)
}

func writeResult(w io.Writer, res client.Result) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(res)
}
