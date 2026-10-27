package app

import (
	"context"
	"encoding/json"
	"errors"
	"openwrt-wifi-guard/internal/config"
	"time"

	"openwrt-wifi-guard/internal/rpc"
)

// configureLoop is the sole configuration writer. UCI subprocesses, readback
// and fsync never run under configMu or on the Wi-Fi registry owner goroutine.
// Jobs are bounded and ordered; each takes its effective baseline at execution.
func (s *Service) configureLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case job := <-s.configureQueue:
			if job.Context.Err() != nil {
				job.Reply <- rpc.Result{Err: errRequest}
				continue
			}
			ctx, cancel := context.WithCancel(job.Context)
			unlink := context.AfterFunc(s.ctx, cancel)
			value, err := s.configure(ctx, job.Params)
			unlink()
			cancel()
			job.Reply <- rpc.Result{Value: value, Err: err}
		}
	}
}

// recoverConfiguration is intentionally tied to service lifetime, not the
// potentially expired RPC. A failed Save may already have published a rename.
func (s *Service) recoverConfiguration() bool {
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	actual, err := s.store.Load(ctx)
	if err == nil {
		n, prepareErr := s.prepare(actual)
		if prepareErr == nil {
			s.apply(actual, n)
			return true
		}
	}
	// Unknown disk state must not keep sending with old credentials. Public
	// settings fail closed until a subsequent configuration readback succeeds.
	s.pauseConfiguration()
	s.logger.Error("telegram_configuration_state_unconfirmed")
	return false
}
func (s *Service) pauseConfiguration() {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.cancelGeneration != nil {
		s.cancelGeneration()
	}
	s.sender = nil
	s.testSender = nil
	s.configUncertain = true
}
func (s *Service) configure(ctx context.Context, data json.RawMessage) (any, error) {
	var p struct {
		Enabled *bool   `json:"enabled"`
		ChatID  *string `json:"chat_id"`
		Token   *string `json:"token"`
		Clear   bool    `json:"clear_token"`
	}
	if decode(data, &p) != nil || ctx.Err() != nil {
		return nil, errRequest
	}
	s.configMu.RLock()
	uncertain := s.configUncertain
	s.configMu.RUnlock()
	if uncertain && !s.recoverConfiguration() {
		return nil, errRequest
	}
	s.configMu.RLock()
	before := s.settings
	s.configMu.RUnlock()
	next := before
	if p.Enabled != nil {
		next.Enabled = *p.Enabled
	}
	if p.ChatID != nil {
		next.ChatID = *p.ChatID
	}
	if p.Clear {
		next.Token = ""
	}
	if p.Token != nil && *p.Token != "" {
		next.Token = *p.Token
	}
	if next == before {
		return map[string]bool{"ok": true}, nil
	}
	sender, err := s.prepare(next)
	if err != nil {
		return nil, errRequest
	}
	if err := s.store.Save(ctx, next); err != nil {
		var published *config.PublishedError
		if errors.As(err, &published) {
			s.pauseConfiguration()
		}
		s.recoverConfiguration()
		s.logger.Error("telegram_configuration_save_failed")
		return nil, errRequest
	}
	// A successful publish must be reflected live even if the RPC was canceled
	// immediately after commit. Cancellation cannot safely roll back a rename.
	s.apply(next, sender)
	return map[string]bool{"ok": true}, nil
}

func (s *Service) startTest(c rpc.Command) {
	if decode(c.Params, &struct{}{}) != nil {
		c.Reply <- rpc.Result{Err: errRequest}
		return
	}
	s.configMu.Lock()
	if s.configUncertain {
		s.configMu.Unlock()
		c.Reply <- rpc.Result{Err: errRequest}
		return
	}
	sender := s.testSender
	if sender == nil {
		settings := s.settings
		settings.Enabled = true
		var err error
		sender, err = s.prepare(settings)
		if err != nil {
			s.configMu.Unlock()
			c.Reply <- rpc.Result{Err: errRequest}
			return
		}
		s.testSender = sender
	}
	generation := s.generation
	s.wg.Add(1)
	s.configMu.Unlock()
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithCancel(generation)
		defer cancel()
		unlink := context.AfterFunc(c.Context, cancel)
		defer unlink()
		if c.Context.Err() != nil {
			cancel()
		}
		err := sender.SendTest(ctx)
		if err != nil {
			s.logger.Warn("telegram_test_failed")
		}
		c.Reply <- rpc.Result{Value: map[string]bool{"ok": err == nil}, Err: err}
	}()
}
