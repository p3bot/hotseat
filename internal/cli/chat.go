// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

const (
	envAddress   = "HOTSEAT_ADDRESS"
	envTokenFile = "HOTSEAT_TOKEN_FILE"

	flagAddress   = "address"
	flagTokenFile = "token-file"

	// defaultReadLimit keeps a bare read well under bus.MaxReadLimit, because a page of large bodies floods a reader.
	defaultReadLimit = 100
)

const chatResultText = `Print one text result and exit. outcome is ok, timeout, refused, unavailable, or connection_failure.

A field on one line is "name: value". A message body is always a byte count and then those bytes, and so is any other value that contains a newline. The count keeps an empty body, more than one line, and a line that looks like a field inside the value.

` + exitStatusText + `

--address or HOTSEAT_ADDRESS names the bus. The default is ` + bus.DefaultListen + `. --token-file or HOTSEAT_TOKEN_FILE reads the shared capability token for a bus that is not on loopback. The flag wins over the variable. The token is not a flag. Omit both for a loopback bus.`

// exitStatusText is the exit status table every chat command states in its help.
const exitStatusText = `Exit status follows the outcome: ok 0, a usage or startup error 1, refused 2, timeout 3, unavailable and connection_failure 4. SIGINT and SIGTERM exit 128 plus the signal number.`

func newChatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "chat",
		Short:         "Create, send to, read, and wait on one chat",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		Long: `Act on one chat on a bus that is already running. Every verb except list
names the chat as its first argument. A missing or extra argument is a usage
error on stderr with nothing on stdout.

` + exitStatusText + `

--address or HOTSEAT_ADDRESS names the bus, default ` + bus.DefaultListen + `.
--token-file or HOTSEAT_TOKEN_FILE reads the capability token for a bus that
is not on loopback. The flag wins over the variable. The token is not a flag.`,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("choose create, list, close, status, send, read, wait, attend, join, or members")
		},
	}
	cmd.PersistentFlags().String(flagAddress, bus.DefaultListen, "bus address (env "+envAddress+")")
	cmd.PersistentFlags().String(flagTokenFile, "", "file holding the capability token; the token is not a flag (env "+envTokenFile+")")
	cmd.AddCommand(
		newCreateCmd(),
		newListCmd(),
		newCloseCmd(),
		newStatusCmd(),
		newSendCmd(),
		newReadCmd(),
		newWaitCmd(),
		newAttendCmd(),
		newJoinCmd(),
		newMembersCmd(),
	)
	return cmd
}

func newCreateCmd() *cobra.Command {
	return newChatVerb("create <name>", "Create a chat", `  hotseat chat create job
  hotseat chat --address 127.0.0.1:4727 create job`, chatArgs("name"), func(cmd *cobra.Command, args []string) error {
		return call(cmd, "create", client.CreateRequest{Name: args[0]})
	})
}

func newListCmd() *cobra.Command {
	return newChatVerb("list", "List every chat", `  hotseat chat list`, chatArgs(), func(cmd *cobra.Command, _ []string) error {
		return call(cmd, "list", struct{}{})
	})
}

func newCloseCmd() *cobra.Command {
	return newChatVerb("close <name>", "Close a chat", `  hotseat chat close job`, chatArgs("name"), func(cmd *cobra.Command, args []string) error {
		return call(cmd, "close", client.CloseRequest{Chat: args[0]})
	})
}

func newSendCmd() *cobra.Command {
	var from, body, key, kind, bodyFile string
	var to []string
	var re int64
	cmd := newChatVerb("send <name>", "Send a message to a chat", `  hotseat chat send job --from alice --kind say --to bob --body hello
  hotseat chat send job --from alice --kind say --to bob --body hello --txid 1
  hotseat chat send job --from bob --kind say --to alice --body done --re 1
  hotseat chat send job --from alice --kind say --to bob --body-file note.txt`, chatArgs("name"), func(cmd *cobra.Command, args []string) error {
		text, err := sendBody(cmd, body, bodyFile)
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("txid") {
			key, err = mintTxID()
			if err != nil {
				return err
			}
		}
		if err := writeTxID(cmd.ErrOrStderr(), key); err != nil {
			return err
		}
		return call(cmd, "send", client.SendRequest{
			Chat: args[0],
			From: from,
			To:   to,
			Body: text,
			TxID: key,
			Kind: kind,
			Re:   changedRe(cmd, re),
		})
	})
	cmd.Long += `

The txid names this attempt. A send with a txid already stored in the chat is
not stored again and prints already_stored. When --txid is omitted, send mints
a random 10-character hex id and writes it to stderr, so a retry without
--txid is not idempotent and can store the message twice. A caller that
retries passes --txid.`
	cmd.Flags().StringVar(&from, "from", "", "sender name")
	cmd.Flags().StringVar(&kind, "kind", "", "message kind")
	cmd.Flags().StringArrayVar(&to, "to", nil, "addressee; repeat for more than one; omit for none")
	cmd.Flags().StringVar(&body, "body", "", "message body")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "file to read the body from; - reads stdin")
	cmd.Flags().StringVar(&key, "txid", "", "transaction id; a random 10 hex characters when omitted, so pass one to retry")
	cmd.Flags().Int64Var(&re, "re", 0, "seq of an earlier message of this chat that this one responds to; omit for none")
	markRequired(cmd, "from", "kind")
	return cmd
}

func newReadCmd() *cobra.Command {
	var to oneString
	var kinds []string
	var cursor, limit int64
	cmd := newChatVerb("read <name>", "Read messages after a cursor", `  hotseat chat read job
  hotseat chat read job --cursor 40 --limit 50 --to bob --kind say`, chatArgs("name"), func(cmd *cobra.Command, args []string) error {
		return call(cmd, "read", client.ReadRequest{
			Chat:   args[0],
			Cursor: cursor,
			Limit:  limit,
			Name:   to.value(),
			Kinds:  changedKinds(cmd, kinds),
		})
	})
	cmd.Long += `

--cursor defaults to 0, which has seen nothing. --limit defaults to ` + strconv.Itoa(defaultReadLimit) + `, and the
bus refuses a limit above ` + strconv.Itoa(bus.MaxReadLimit) + `. An ok read prints more: true when a further
matching message follows the page, and more: false otherwise. Page on more,
with the last seq printed as the next --cursor.`
	cmd.Flags().Int64Var(&cursor, "cursor", 0, "last finished sequence; 0 has seen nothing")
	cmd.Flags().Int64Var(&limit, "limit", defaultReadLimit, "maximum number of messages")
	cmd.Flags().Var(&to, "to", "keep messages addressed to this participant; one value; omit to read every message")
	cmd.Flags().StringArrayVar(&kinds, "kind", nil, "kind to keep; repeat to list more than one; omit to keep every kind")
	return cmd
}

func newWaitCmd() *cobra.Command {
	var to oneString
	var deadline string
	var kinds []string
	var cursor int64
	cmd := newChatVerb("wait <name>", "Wait for a matching message", `  hotseat chat wait job --cursor 0 --to bob --kind turn --deadline 30s`, chatArgs("name"), func(cmd *cobra.Command, args []string) error {
		return call(cmd, "wait", client.WaitRequest{
			Chat:     args[0],
			Cursor:   cursor,
			Name:     to.value(),
			Deadline: changedString(cmd, "deadline", deadline),
			Kinds:    changedKinds(cmd, kinds),
		})
	})
	cmd.Long += `

--cursor is required, so a waiter that forgot it is not handed an old message.`
	cmd.Flags().Int64Var(&cursor, "cursor", 0, "last finished sequence; 0 has seen nothing")
	cmd.Flags().Var(&to, "to", "wait for a message addressed to this participant; one value; omit to wait for the next message")
	cmd.Flags().StringArrayVar(&kinds, "kind", nil, "kind to keep; repeat to list more than one; omit to keep every kind")
	cmd.Flags().StringVar(&deadline, "deadline", "", "wait limit: a duration such as 30s, or an RFC3339 end time; omit to wait without one")
	markRequired(cmd, "cursor")
	return cmd
}

func newJoinCmd() *cobra.Command {
	cmd := newChatVerb("join <name> <member>", "Record that a roster member has joined", `  hotseat chat join job alpha`, chatArgs("name", "member"), func(cmd *cobra.Command, args []string) error {
		return call(cmd, "join", client.JoinRequest{Chat: args[0], Name: args[1]})
	})
	cmd.Long += `

join writes the member row of a roster name. It is not a message.`
	return cmd
}

func newMembersCmd() *cobra.Command {
	return newChatVerb("members <name>", "List member rows", `  hotseat chat members job`, chatArgs("name"), func(cmd *cobra.Command, args []string) error {
		return call(cmd, "members", client.MembersRequest{Chat: args[0]})
	})
}

// newChatVerb is one chat verb. Its --address and --token-file are the chat group's persistent flags.
func newChatVerb(use, short, example string, args cobra.PositionalArgs, run func(cmd *cobra.Command, args []string) error) *cobra.Command {
	return &cobra.Command{
		Use:           use,
		Short:         short,
		Long:          chatResultText,
		Example:       example,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          args,
		RunE:          run,
	}
}

// chatArgs requires exactly the named positionals, each non-empty, so an empty chat name never reaches the bus.
func chatArgs(names ...string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != len(names) {
			want := "no arguments"
			if len(names) > 0 {
				want = "<" + strings.Join(names, "> <") + ">"
			}
			return fmt.Errorf("%s takes %s; got %d", cmd.CommandPath(), want, len(args))
		}
		for i, arg := range args {
			if arg == "" {
				return fmt.Errorf("%s: <%s> is empty", cmd.CommandPath(), names[i])
			}
		}
		return nil
	}
}

// oneString is a flag that takes one value. pflag's string flag keeps the last of several, which would drop a filter silently.
type oneString struct {
	v   string
	set bool
}

func (s *oneString) String() string { return s.v }

func (s *oneString) Type() string { return "string" }

func (s *oneString) Set(v string) error {
	if s.set {
		return errors.New("takes one value; pass it once")
	}
	s.v, s.set = v, true
	return nil
}

// value is the flag value, including an empty one, or nil when the flag was not passed.
func (s *oneString) value() *string {
	if !s.set {
		return nil
	}
	v := s.v
	return &v
}

// addAddress declares --address on a command that is not under chat.
func addAddress(cmd *cobra.Command) {
	cmd.Flags().String(flagAddress, bus.DefaultListen, "bus address (env "+envAddress+")")
}

// addTokenFile declares --token-file on a command that is not under chat.
func addTokenFile(cmd *cobra.Command) {
	cmd.Flags().String(flagTokenFile, "", "file holding the capability token; the token is not a flag (env "+envTokenFile+")")
}

// resolveAddress is --address when passed, else a non-empty HOTSEAT_ADDRESS, else the default.
// An explicit empty flag is an error, so the flag is not replaced by the default.
func resolveAddress(cmd *cobra.Command) (string, error) {
	addr, set, err := flagOrEnv(cmd, flagAddress, envAddress)
	if err != nil {
		return "", err
	}
	if set && addr == "" {
		return "", errors.New("--address is empty")
	}
	if addr == "" {
		return bus.DefaultListen, nil
	}
	return addr, nil
}

// resolveTokenFile is --token-file when passed, else a non-empty HOTSEAT_TOKEN_FILE. set is false when neither names a file.
func resolveTokenFile(cmd *cobra.Command) (path string, set bool, err error) {
	return flagOrEnv(cmd, flagTokenFile, envTokenFile)
}

// resolveToken reads the token file resolveTokenFile names, or returns no token when none is named.
func resolveToken(cmd *cobra.Command) (string, error) {
	path, set, err := resolveTokenFile(cmd)
	if err != nil || !set {
		return "", err
	}
	return bus.ReadTokenFile(path)
}

// flagOrEnv is the flag value when passed, else a non-empty variable. An empty variable counts as unset.
func flagOrEnv(cmd *cobra.Command, flag, env string) (string, bool, error) {
	if cmd.Flags().Changed(flag) {
		v, err := cmd.Flags().GetString(flag)
		return v, true, err
	}
	if v := os.Getenv(env); v != "" {
		return v, true, nil
	}
	v, err := cmd.Flags().GetString(flag)
	return v, false, err
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

// changedRe omits the field when --re was not passed.
func changedRe(cmd *cobra.Command, re int64) *int64 {
	if !cmd.Flags().Changed("re") {
		return nil
	}
	return &re
}

// changedKinds omits the field when --kind was not passed.
// A passed flag sends the list, including one that is empty.
func changedKinds(cmd *cobra.Command, kinds []string) *[]string {
	if !cmd.Flags().Changed("kind") {
		return nil
	}
	if kinds == nil {
		kinds = []string{}
	}
	return &kinds
}

// sendBody takes exactly one source. A file is how a body outgrows one
// command argument; "-" reads stdin so the client still writes nothing.
func sendBody(cmd *cobra.Command, inline, path string) (string, error) {
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

// mintTxID is one attempt id: 10 lowercase hex characters from 5 random bytes.
func mintTxID() (string, error) {
	var buf [5]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// writeTxID prints the attempt id before the call so a retry can reuse it.
func writeTxID(w io.Writer, id string) error {
	if _, err := fmt.Fprintf(w, "txid: %s\n", id); err != nil {
		return err
	}
	if f, ok := w.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

// call makes one bus call, prints its result, and exits by its outcome.
func call(cmd *cobra.Command, op string, body any) error {
	address, err := resolveAddress(cmd)
	if err != nil {
		return err
	}
	token, err := resolveToken(cmd)
	if err != nil {
		return err
	}
	res, err := client.Do(cmd.Context(), address, op, token, body)
	if err != nil {
		return err
	}
	if err := writeResult(cmd.OutOrStdout(), res); err != nil {
		return err
	}
	return outcomeExit(res.Outcome)
}
