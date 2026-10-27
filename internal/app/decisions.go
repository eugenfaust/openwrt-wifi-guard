package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"openwrt-wifi-guard/internal/config"
	"openwrt-wifi-guard/internal/devices"
	"openwrt-wifi-guard/internal/rpc"
	"openwrt-wifi-guard/internal/storage"
	"openwrt-wifi-guard/internal/telegram"
)

// decisionContextKey authenticates in-process commands independently of the RPC
// bridge whitelist. Context values cannot be supplied through JSON RPC.
type decisionContextKey struct{}

func (s *Service) dispatchDecision(ctx context.Context, method string, data json.RawMessage) (any, error) {
	return rpc.Dispatch(s.commands)(context.WithValue(ctx, decisionContextKey{}, s), method, data)
}

// checkPolicy reserves a validated revision until completion. Superseding intent
// is rejected while I/O is active, so a stale deny cannot run after an accepted
// unblock. Queued obsolete revisions are rejected before any policy I/O.
func (s *Service) checkPolicy(data json.RawMessage) (bool, error) {
	var p struct {
		MAC      string
		Revision uint64
		Deny     bool
	}
	if decode(data, &p) != nil || s.policySet == nil {
		return false, errRequest
	}
	if err := s.confirmDecisionDurability(); err != nil {
		return false, err
	}
	d := s.registry.State.Devices[p.MAC]
	if d == nil || p.Revision == 0 || d.PolicyRevision != p.Revision || d.DesiredDeny != p.Deny || s.policyActive[p.MAC] != 0 || (p.Deny && s.protected[p.MAC]) {
		return false, errRequest
	}
	if s.policyActive == nil {
		s.policyActive = make(map[string]uint64)
	}
	s.policyActive[p.MAC] = p.Revision
	return true, nil
}

type actionEncoder interface {
	EncodeAction(telegram.ActionClaims) (string, error)
}
type actionRequest struct {
	Claims telegram.ActionClaims
	Scope  string
}
type policyJob struct {
	MAC      string
	Revision uint64
	Deny     bool
	Telegram bool
	Reply    chan rpc.Result `json:"-"`
}
type policyCompletion struct {
	MAC      string
	Revision uint64
	Deny     bool
	Applied  bool
	Failed   bool
	Telegram bool
}

func credentialScope(c config.Settings) string {
	digest := sha256.Sum256([]byte("wifi-guard/review-scope/v1\x00" + c.Token + "\x00" + c.ChatID))
	return hex.EncodeToString(digest[:])
}

// PrepareAlert runs on the registry owner before the monitor durably saves the
// new device. Alert only publishes the already saved review; it never creates it.
func (s *Service) PrepareAlert(e devices.Event) error {
	s.enrichHostnames()
	s.configMu.RLock()
	c, uncertain := s.settings, s.configUncertain
	s.configMu.RUnlock()
	if !s.actions || !c.Enabled || uncertain {
		return nil
	}
	return s.registry.EnsureReview(e.MAC, credentialScope(c), e.At)
}
func (s *Service) actionHandler(c config.Settings) func(context.Context, telegram.ActionClaims) (telegram.ActionResult, error) {
	scope := credentialScope(c)
	return func(ctx context.Context, claims telegram.ActionClaims) (telegram.ActionResult, error) {
		params, _ := json.Marshal(actionRequest{Claims: claims, Scope: scope})
		value, err := s.dispatchDecision(ctx, "_telegram_action", params)
		if err != nil {
			return telegram.ActionResult{}, err
		}
		result, ok := value.(telegram.ActionResult)
		if !ok {
			return telegram.ActionResult{}, errRequest
		}
		return result, nil
	}
}
func (s *Service) reviewKeyboard(sender Sender, d devices.Device) ([][]telegram.InlineButton, error) {
	encoder, ok := sender.(actionEncoder)
	if !ok || d.ReviewNonce == 0 {
		return nil, nil
	}
	type button struct {
		label  string
		action telegram.Action
	}
	var buttons []button
	switch d.ReviewStage {
	case "review":
		buttons = []button{{"Доверять", telegram.ActionTrust}, {"Заблокировать", telegram.ActionBlockPrompt}}
	case "block_confirm":
		buttons = []button{{"Да, отключить и запретить", telegram.ActionBlockConfirm}, {"Отмена", telegram.ActionCancel}}
	case "blocked":
		buttons = []button{{"Разблокировать", telegram.ActionUnblockPrompt}}
	case "unblock_confirm":
		buttons = []button{{"Да, снять мой запрет", telegram.ActionUnblockConfirm}, {"Отмена", telegram.ActionCancel}}
	default:
		return [][]telegram.InlineButton{}, nil
	}
	row := make([]telegram.InlineButton, 0, len(buttons))
	for _, b := range buttons {
		data, err := encoder.EncodeAction(telegram.ActionClaims{Action: b.action, MAC: d.MAC, Nonce: d.ReviewNonce, Issued: d.ReviewIssued})
		if err != nil {
			return nil, err
		}
		row = append(row, telegram.InlineButton{Text: b.label, Data: data})
	}
	return [][]telegram.InlineButton{row}, nil
}
func actionName(a telegram.Action) string {
	switch a {
	case telegram.ActionTrust:
		return "trust"
	case telegram.ActionBlockPrompt:
		return "block_prompt"
	case telegram.ActionBlockConfirm:
		return "block_confirm"
	case telegram.ActionUnblockPrompt:
		return "unblock_prompt"
	case telegram.ActionUnblockConfirm:
		return "unblock_confirm"
	case telegram.ActionCancel:
		return "cancel"
	}
	return ""
}
func (s *Service) decisionResult(d devices.Device, text string) telegram.ActionResult {
	s.configMu.RLock()
	sender := s.sender
	s.configMu.RUnlock()
	keys, _ := s.reviewKeyboard(sender, d)
	return telegram.ActionResult{Text: text, Name: d.DisplayName(), Keyboard: keys}
}

// persistDevice restores only an unpublished mutation. Possibly published state
// remains live/dirty; retry the full save before allowing enforcement or ack.
func (s *Service) persistDevice(d *devices.Device, before devices.Device, history []devices.Event, dirty, fresh bool) error {
	if err := s.save(s.registry.State); err != nil {
		var published *storage.PublishedError
		if errors.As(err, &published) {
			s.decisionsUncertain = true
			s.registry.Dirty = true
			return errRequest
		}
		*d = before
		s.registry.State.History = history
		s.registry.Dirty = dirty
		s.registry.Fresh = fresh
		return errRequest
	}
	s.decisionsUncertain = false
	s.registry.Dirty = false
	s.registry.Fresh = false
	return nil
}
func (s *Service) confirmDecisionDurability() error {
	if !s.decisionsUncertain {
		return nil
	}
	if err := s.save(s.registry.State); err != nil {
		return errRequest
	}
	s.decisionsUncertain = false
	s.registry.Dirty = false
	s.registry.Fresh = false
	return nil
}
func (s *Service) startAction(c rpc.Command) {
	result, err, deferred := s.acceptAction(c)
	if !deferred {
		c.Reply <- rpc.Result{Value: result, Err: err}
	}
}
func (s *Service) acceptAction(c rpc.Command) (telegram.ActionResult, error, bool) {
	rejected := telegram.ActionResult{Text: "Кнопка устарела или решение уже изменено.", Keyboard: [][]telegram.InlineButton{}}
	var p actionRequest
	if decode(c.Params, &p) != nil || !s.actions {
		return rejected, nil, false
	}
	if err := s.confirmDecisionDurability(); err != nil {
		return telegram.ActionResult{}, err, false
	}
	s.configMu.RLock()
	settings, uncertain := s.settings, s.configUncertain
	s.configMu.RUnlock()
	if uncertain || !settings.Enabled || p.Scope != credentialScope(settings) {
		return rejected, nil, false
	}
	d := s.registry.State.Devices[p.Claims.MAC]
	if d == nil || d.ReviewNonce == 0 || d.ReviewNonce != p.Claims.Nonce || d.ReviewScope != p.Scope {
		return rejected, nil, false
	}
	name := actionName(p.Claims.Action)
	if name == "" {
		return rejected, nil, false
	}
	if name == "block_prompt" || name == "block_confirm" {
		if s.protected[d.MAC] {
			return s.decisionResult(*d, "Это устройство защищено от блокировки."), nil, false
		}
		if !s.allowBlocking || s.policySet == nil {
			return s.decisionResult(*d, "Постоянная блокировка не включена на этом роутере."), nil, false
		}
	}
	policyChange := name == "block_confirm" || name == "unblock_confirm"
	if policyChange && (s.policySet == nil || s.policyActive[d.MAC] != 0 || len(s.policyQueue) == cap(s.policyQueue)) {
		return telegram.ActionResult{}, errRequest, false
	}
	before := *d
	history := append([]devices.Event(nil), s.registry.State.History...)
	dirty, fresh := s.registry.Dirty, s.registry.Fresh
	if err := s.registry.Decide(d.MAC, p.Claims.Nonce, p.Scope, name, time.Now().UTC()); err != nil {
		return rejected, nil, false
	}
	if err := s.persistDevice(d, before, history, dirty, fresh); err != nil {
		return telegram.ActionResult{}, err, false
	}
	if policyChange {
		s.policyQueue <- policyJob{MAC: d.MAC, Revision: d.PolicyRevision, Deny: d.DesiredDeny, Telegram: true, Reply: c.Reply}
		return telegram.ActionResult{}, nil, true
	}
	text := "Решение отменено."
	switch name {
	case "trust":
		text = "Устройство отмечено доверенным. Сетевые правила не изменены."
	case "block_prompt":
		text = fmt.Sprintf("Запретить Wi-Fi для MAC %s на управляемых точках доступа? Устройство будет отключено. Запрет сохранится до ручной разблокировки.", d.MAC)
	case "unblock_prompt":
		text = fmt.Sprintf("Снять собственный запрет WiFi Guard для %s? Чужие MAC-правила останутся в силе.", d.MAC)
	}
	return s.decisionResult(*d, text), nil, false
}
func (s *Service) startUnblock(c rpc.Command) {
	var p struct {
		MAC      string `json:"mac"`
		Revision uint64 `json:"revision"`
	}
	if decode(c.Params, &p) != nil || s.policySet == nil || len(s.policyQueue) == cap(s.policyQueue) {
		c.Reply <- rpc.Result{Err: errRequest}
		return
	}
	mac, err := devices.NormalizeMAC(p.MAC)
	if err != nil {
		c.Reply <- rpc.Result{Err: errRequest}
		return
	}
	d := s.registry.State.Devices[mac]
	if d == nil || s.policyActive[mac] != 0 || !d.DesiredDeny || d.PolicyRevision != p.Revision || d.PolicyRevision == ^uint64(0) {
		c.Reply <- rpc.Result{Err: errRequest}
		return
	}
	if s.confirmDecisionDurability() != nil {
		c.Reply <- rpc.Result{Err: errRequest}
		return
	}
	before := *d
	history := append([]devices.Event(nil), s.registry.State.History...)
	dirty, fresh := s.registry.Dirty, s.registry.Fresh
	d.DesiredDeny = false
	d.PolicyRevision++
	d.PolicyPending = true
	d.ReviewNonce = 0
	d.ReviewScope = ""
	d.ReviewStage = ""
	d.ReviewIssued = time.Time{}
	if d.Trust == "blocked" {
		d.Trust = "unknown"
	}
	s.registry.Dirty = true
	if err := s.persistDevice(d, before, history, dirty, fresh); err != nil {
		c.Reply <- rpc.Result{Err: err}
		return
	}
	s.policyQueue <- policyJob{MAC: d.MAC, Revision: d.PolicyRevision, Deny: false, Reply: c.Reply}
}
func (s *Service) policySnapshot() ([]policyJob, error) {
	if err := s.confirmDecisionDurability(); err != nil {
		return nil, err
	}
	jobs := make([]policyJob, 0)
	for _, d := range s.registry.State.Devices {
		if d.PolicyRevision > 0 {
			jobs = append(jobs, policyJob{MAC: d.MAC, Revision: d.PolicyRevision, Deny: d.DesiredDeny})
		}
	}
	return jobs, nil
}
func (s *Service) finishPolicy(data json.RawMessage) (any, error) {
	var p policyCompletion
	if decode(data, &p) != nil {
		return nil, errRequest
	}
	if s.policyActive[p.MAC] != p.Revision || p.Revision == 0 {
		return nil, errRequest
	}
	delete(s.policyActive, p.MAC)
	d := s.registry.State.Devices[p.MAC]
	if d == nil || d.PolicyRevision != p.Revision || d.DesiredDeny != p.Deny {
		if p.Telegram {
			return telegram.ActionResult{Text: "Решение уже изменено.", Keyboard: [][]telegram.InlineButton{}}, nil
		}
		return nil, errRequest
	}
	pending := p.Failed || !p.Applied
	if d.PolicyPending != pending {
		before := *d
		history := append([]devices.Event(nil), s.registry.State.History...)
		dirty, fresh := s.registry.Dirty, s.registry.Fresh
		d.PolicyPending = pending
		s.registry.Dirty = true
		if err := s.persistDevice(d, before, history, dirty, fresh); err != nil {
			return nil, err
		}
	}
	text := "Запрет WiFi Guard снят. Независимые правила администратора могут сохраняться."
	if p.Deny {
		text = "Постоянный запрет по MAC сохранён и применён на управляемых точках доступа."
	}
	if p.Failed || !p.Applied {
		text = "Решение сохранено, но применение не подтверждено. Проверьте состояние в LuCI; доступна ручная разблокировка."
	}
	if p.Telegram {
		return s.decisionResult(*d, text), nil
	}
	return map[string]bool{"ok": !p.Failed && p.Applied, "pending": d.PolicyPending}, nil
}
func (s *Service) policyLoop(initial []policyJob) {
	defer s.wg.Done()
	// Journal recovery is independent of registry revisions (including a crash
	// between policy publication and registry bookkeeping).
	if s.policyReconcile != nil {
		ctx, cancel := context.WithTimeout(s.ctx, 25*time.Second)
		if err := s.policyReconcile(ctx); err != nil {
			s.logger.Error("policy_recovery_unconfirmed")
		}
		cancel()
	}
	execute := func(job policyJob) {
		check, _ := json.Marshal(struct {
			MAC      string
			Revision uint64
			Deny     bool
		}{job.MAC, job.Revision, job.Deny})
		value, err := s.dispatchDecision(s.ctx, "_policy_check", check)
		if err != nil || value != true {
			if job.Reply != nil {
				job.Reply <- rpc.Result{Err: errRequest}
			}
			return
		}
		ctx, cancel := context.WithTimeout(s.ctx, 25*time.Second)
		defer cancel()
		desired, applied, err := s.policySet(ctx, job.MAC, job.Deny)
		completion, _ := json.Marshal(policyCompletion{MAC: job.MAC, Revision: job.Revision, Deny: job.Deny, Applied: applied, Failed: err != nil || desired != job.Deny, Telegram: job.Telegram})
		// Complete on service lifetime, not the departed caller or I/O timeout.
		// Await owner acknowledgement before validating the next serialized job.
		value, doneErr := s.dispatchDecision(s.ctx, "_policy_done", completion)
		if job.Reply != nil {
			job.Reply <- rpc.Result{Value: value, Err: doneErr}
		}
	}
	for _, job := range initial {
		if s.ctx.Err() != nil {
			return
		}
		execute(job)
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case job := <-s.policyQueue:
			execute(job)
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
			value, err := s.dispatchDecision(ctx, "_policy_snapshot", json.RawMessage(`{}`))
			cancel()
			if err == nil {
				if jobs, ok := value.([]policyJob); ok {
					for _, job := range jobs {
						if s.ctx.Err() != nil {
							return
						}
						execute(job)
					}
				}
			}
		}
	}
}
