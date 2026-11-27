// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"io"

	"github.com/spf13/cobra"

	"github.com/p3bot/hotseat/internal/bus"
	"github.com/p3bot/hotseat/internal/client"
)

const (
	ownerName = "owner"
	kindAsk   = "ask"
	kindError = "error"
	kindDone  = "done"
)

func newAttendCmd() *cobra.Command {
	var cursor int64
	cmd := newChatVerb("attend <name>", "Wait as owner for ask, error, and done", `  hotseat chat attend job
  hotseat chat attend job --cursor 4`, chatArgs("name"), func(cmd *cobra.Command, args []string) error {
		cfg, err := attendSetup(cmd, args[0], cursor)
		if err != nil {
			return err
		}
		return attend(cmd.Context(), cfg, cmd.OutOrStdout())
	})
	cmd.Long = `Wait as owner on one chat and print ask, error, and done. attend does not
start the bus, open the database, launch a process, or send. It does not
write a member row and it does not resume a chief.

--cursor is the last finished sequence. The default is 0, which has seen
nothing. The next wait uses the match sequence just printed. A new process
reads from sequence 0 unless --cursor is set.

Each match is one text result. The bus span may contain other kinds. attend
prints the matching message: the chat name, the match sequence, kind, from,
to in stored order, and the body. A say, a turn, and every other kind are not
printed. A join is a member row, not a message, so it never appears. An ask
or an error is printed and attend waits again. There is no deadline. A done
addressed to owner is printed and attend exits 0, including a done from a
name other than the acting seat. A done that is not addressed to owner does
not end attend.

A call that answers other than ok prints its result and exits by its
outcome: refused 2, timeout 3, unavailable and connection_failure 4. An ok
answer that is not an ask, error, or done exits 1. A usage error prints no
stdout and exits 1. SIGINT and SIGTERM exit 128 plus the signal number.

--address or HOTSEAT_ADDRESS names the bus, default ` + bus.DefaultListen + `.
--token-file or HOTSEAT_TOKEN_FILE reads the capability token. The flag wins.
The token value is not printed.`
	cmd.Flags().Int64Var(&cursor, "cursor", 0, "last finished sequence; 0 has seen nothing")
	return cmd
}

type attendConfig struct {
	Chat    string
	Cursor  int64
	Address string
	Token   string
}

func attendSetup(cmd *cobra.Command, chat string, cursor int64) (attendConfig, error) {
	address, err := resolveAddress(cmd)
	if err != nil {
		return attendConfig{}, err
	}
	token, err := resolveToken(cmd)
	if err != nil {
		return attendConfig{}, err
	}
	return attendConfig{Chat: chat, Cursor: cursor, Address: address, Token: token}, nil
}

func attend(ctx context.Context, cfg attendConfig, w io.Writer) error {
	name := ownerName
	kinds := []string{kindAsk, kindError, kindDone}
	req := client.WaitRequest{
		Chat:  cfg.Chat,
		Name:  &name,
		Kinds: &kinds,
	}
	cursor := cfg.Cursor
	for {
		req.Cursor = cursor
		res, err := client.Do(ctx, cfg.Address, "wait", cfg.Token, req)
		if err != nil {
			return err
		}
		if res.Outcome != bus.OutcomeOK {
			if err := writeResult(w, res); err != nil {
				return err
			}
			return reportedExit{code: exitCode(res.Outcome)}
		}
		msg, ok := attendMatch(res)
		if !ok {
			if err := writeResult(w, res); err != nil {
				return err
			}
			return reportedExit{code: ExitFailure}
		}
		seq := msg.Seq
		if err := writeResult(w, client.Result{
			Outcome:  bus.OutcomeOK,
			MatchSeq: &seq,
			Chat:     &client.Chat{Name: cfg.Chat},
			Message:  &msg,
		}); err != nil {
			return err
		}
		if msg.Kind == kindDone {
			return nil
		}
		cursor = msg.Seq
	}
}

func attendMatch(res client.Result) (client.Message, bool) {
	if res.MatchSeq == nil {
		return client.Message{}, false
	}
	want := *res.MatchSeq
	var found client.Message
	ok := false
	if res.Messages != nil {
		for _, msg := range *res.Messages {
			if msg.Seq == want {
				found = msg
				ok = true
			}
		}
	}
	if res.Message != nil && res.Message.Seq == want {
		found = *res.Message
		ok = true
	}
	if !ok || !attendKind(found.Kind) {
		return client.Message{}, false
	}
	return found, true
}

func attendKind(kind string) bool {
	switch kind {
	case kindAsk, kindError, kindDone:
		return true
	default:
		return false
	}
}
