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
	cmd := newChatVerb("attend <name>", "Wait as owner for ask, error, and done", `Wait as owner and print each ask, error, and done addressed to owner. After
an ask or an error, attend waits again, with no deadline. A done exits 0.
To pick up where an earlier attend stopped, pass its last match_seq as
--cursor.`, `  hotseat chat attend job
  hotseat chat attend job --cursor 4`, chatArgs("name"), func(cmd *cobra.Command, args []string) error {
		cfg, err := attendSetup(cmd, args[0], cursor)
		if err != nil {
			return err
		}
		return attend(cmd.Context(), cfg, cmd.OutOrStdout())
	})
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
