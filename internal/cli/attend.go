// Copyright (c) 2026 Grant Carthew
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"errors"
	"io"
	"os"

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
	var f attendFlags
	cmd := &cobra.Command{
		Use:   "attend",
		Short: "Wait as owner for ask, error, and done",
		Long: `Wait as owner on one conversation and print ask, error, and done. attend
does not start the bus, open the database, launch a process, or publish.
It does not write a member row and it does not resume a chief.

--conversation or HOTSEAT_CONVERSATION names the conversation. The flag wins.
--cursor is the last finished sequence. The default is 0, which has seen
nothing. The next wait uses the match sequence just printed. A new process
reads from sequence 0 unless --cursor is set.

Each match is one text result. The bus span may contain other kinds. attend
prints the matching message: the conversation name, the match sequence, kind,
from, to in stored order, and the body. A say, a register, and any other join
are not printed. An ask or an error is printed and attend waits again. There
is no deadline. A done addressed to owner is printed and attend exits zero,
including a done from a name other than the acting seat. A done that is not
addressed to owner does not end attend.

--address and HOTSEAT_ADDRESS default to 127.0.0.1:4727. --token-file and
HOTSEAT_TOKEN_FILE follow hotseat run. The flag wins. The token value is not
printed. A connection failure, a refusal, or an unexpected
outcome exits non-zero. The text result stays on stdout. A usage error prints
no stdout.`,
		Example: `  hotseat attend --conversation job
  hotseat attend --conversation job --cursor 4`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := f.config(cmd)
			if err != nil {
				return err
			}
			return attend(cmd.Context(), cfg, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&f.conversation, "conversation", "", "conversation name (env HOTSEAT_CONVERSATION)")
	cmd.Flags().Int64Var(&f.cursor, "cursor", 0, "last finished sequence; 0 has seen nothing")
	addAddress(cmd, &f.address)
	addTokenFile(cmd)
	return cmd
}

type attendFlags struct {
	conversation string
	cursor       int64
	address      string
}

type attendConfig struct {
	Conversation string
	Cursor       int64
	Address      string
	Token        string
}

func (f attendFlags) config(cmd *cobra.Command) (attendConfig, error) {
	conv, _, err := pickString(cmd, "conversation", "HOTSEAT_CONVERSATION", f.conversation)
	if err != nil {
		return attendConfig{}, err
	}
	if conv == "" {
		return attendConfig{}, errors.New("conversation is required")
	}
	address := f.address
	if !cmd.Flags().Changed("address") {
		if addr, ok := os.LookupEnv("HOTSEAT_ADDRESS"); ok && addr != "" {
			address = addr
		}
	}
	if address == "" {
		address = bus.DefaultListen
	}
	var tokenFile string
	if cmd.Flags().Changed("token-file") {
		tokenFile, err = cmd.Flags().GetString("token-file")
		if err != nil {
			return attendConfig{}, err
		}
	} else if path, ok := os.LookupEnv("HOTSEAT_TOKEN_FILE"); ok && path != "" {
		tokenFile = path
	}
	var token string
	if cmd.Flags().Changed("token-file") || tokenFile != "" {
		token, err = bus.ReadTokenFile(tokenFile)
		if err != nil {
			return attendConfig{}, err
		}
	}
	return attendConfig{
		Conversation: conv,
		Cursor:       f.cursor,
		Address:      address,
		Token:        token,
	}, nil
}

func attend(ctx context.Context, cfg attendConfig, w io.Writer) error {
	name := ownerName
	kinds := []string{kindAsk, kindError, kindDone}
	req := client.WaitRequest{
		Conversation: cfg.Conversation,
		Name:         &name,
		Kinds:        &kinds,
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
			return reportedFailure{}
		}
		msg, ok := attendMatch(res)
		if !ok {
			if err := writeResult(w, res); err != nil {
				return err
			}
			return reportedFailure{}
		}
		seq := msg.Seq
		if err := writeResult(w, client.Result{
			Outcome:      bus.OutcomeOK,
			MatchSeq:     &seq,
			Conversation: &client.Conversation{Name: cfg.Conversation},
			Message:      &msg,
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
