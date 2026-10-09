// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/signinpg"
	"solsynth.local/go-migration/internal/ticketpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/passport/tickets"
)

func TestPGAffiliationExaminerCannotSupplyRecipientRootProof(t *testing.T) {
	f := newAffiliationFixture(t)
	affiliationVerified(t, f, f.actor)
	exam, qs := seedExam(t, f.ticketFixture, true)
	parent, e := f.verifier.Authenticate(context.Background(), f.token)
	if e != nil {
		t.Fatal(e)
	}
	pair, e := f.issuer.IssueSession(context.Background(), f.actor, authority.SessionOptions{Label: "Synthetic delegated manual exam", Scopes: []string{"tests.take"}})
	if e != nil {
		t.Fatal(e)
	}
	proof, e := f.verifier.Authenticate(context.Background(), pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	query(t, f.db, `UPDATE owned_authority_sessions SET parent_id=$1 WHERE id=$2`, parent.Session.ID, proof.Session.ID)
	proof, e = f.verifier.Authenticate(context.Background(), pair.Token)
	if e != nil {
		t.Fatal(e)
	}
	child := f.guard.Bind(context.Background(), proof)
	attempt := ticketExec[tickets.Attempt](t, f.ticketFixture, child, f.actor, tickets.Command{Operation: "attempt.start", Key: exam.Key})
	text := "Synthetic participant answer"
	pending := ticketExec[tickets.Attempt](t, f.ticketFixture, child, f.actor, tickets.Command{Operation: "attempt.submit", ID: attempt.ID, Answers: []tickets.AnswerInput{correctInput(qs[0]), {QuestionID: qs[1].ID, Text: &text}}})
	if pending.Status != tickets.PendingReview {
		t.Fatal(pending)
	}
	var answerID string
	for _, answer := range pending.Answers {
		if answer.QuestionID == qs[1].ID {
			answerID = answer.ID
		}
	}
	graded := ticketExec[tickets.Attempt](t, f.ticketFixture, f.adminCtx, f.admin, tickets.Command{Operation: "answer.review", ID: answerID, Review: &tickets.ReviewInput{IsCorrect: true, AwardedPoints: 1}})
	if graded.Status != tickets.Passed || !graded.ActivationPending {
		t.Fatal("manual grade did not preserve earned pending state", graded)
	}
	for _, account := range []string{f.actor, f.admin} {
		if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_authority_activations WHERE account_id=$1`, account); n != 0 {
			t.Fatal("examiner root proof activated an account", account, n)
		}
	}
	if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_authority_grants WHERE account_id=$1 AND pattern='posts.create'`, f.actor); n != 0 {
		t.Fatal("examiner restored recipient grants", n)
	}
	if _, e = f.repo.Execute(child, f.actor, tickets.Command{Operation: "activation.recheck"}); e == nil {
		t.Fatal("delegated participant completed activation")
	}
	state := ticketExec[tickets.ActivationState](t, f.ticketFixture, f.ctx, f.actor, tickets.Command{Operation: "activation.recheck"})
	if !state.IsActivated || state.ActivationPending {
		t.Fatal(state)
	}
	var originalReceipt string
	if e = f.db.QueryRow(`SELECT policy_sha256 FROM owned_authority_activations WHERE account_id=$1`, f.actor).Scan(&originalReceipt); e != nil {
		t.Fatal(e)
	}
	if e = authoritypg.New(f.db).ReplacePolicy(context.Background(), f.actor, []authority.Rule{{Pattern: "tests.take"}}, nil); e != nil {
		t.Fatal(e)
	}
	for _, policyID := range []string{"synthetic-member-v2", "synthetic-member-v1"} {
		f.config.Activation.Completion = &ticketpg.ActivationCompletion{PolicyID: policyID, Permissions: []string{"posts.create", "posts.react"}}
		fresh := affiliationPool(t, f, database(t))
		state = ticketExec[tickets.ActivationState](t, fresh.ticketFixture, fresh.ctx, fresh.actor, tickets.Command{Operation: "activation.recheck"})
		if !state.IsActivated {
			t.Fatal("historical activation lost", policyID)
		}
		if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_authority_grants WHERE account_id=$1 AND pattern IN('posts.create','posts.react')`, f.actor); n != 0 {
			t.Fatal("policy ABA replay restored revoked grants", policyID, n)
		}
		var receipt string
		if e = f.db.QueryRow(`SELECT policy_sha256 FROM owned_authority_activations WHERE account_id=$1`, f.actor).Scan(&receipt); e != nil || receipt != originalReceipt {
			t.Fatal("policy ABA changed original receipt", policyID, receipt, e)
		}
	}
}

func TestPGAffiliationPrimaryContactChallengeActivationAndRollback(t *testing.T) {
	for _, expire := range []bool{false, true} {
		name := "complete"
		if expire {
			name = "expire_after_wait"
		}
		t.Run(name, func(t *testing.T) {
			f := newAffiliationFixture(t)
			s := affiliationCreate(t, f, true, 1, true)
			store := signinpg.New(f.db, authoritypg.New(f.db), f.guard).WithAffiliation(f.aff).WithActivation(f.repo)
			delivery := &syntheticDelivery{tokens: map[string]string{}}
			seal := sha256.Sum256([]byte("synthetic primary contact activation only"))
			service, e := signin.New(store, f.issuer, f.verifier, signin.Config{SealKey: seal[:], Issuer: "Synthetic affiliation contact", Delivery: delivery, Captcha: syntheticCaptcha{}, Now: f.config.Now, InitialMemberPermissions: []string{"tests.take", "account.contacts.manage"}})
			if e != nil {
				t.Fatal(e)
			}
			username := "synthetic_" + strings.ReplaceAll(authorityUUID(t), "-", "")[:20]
			email := username + "@example.invalid"
			account, e := service.Register(context.Background(), signin.RegisterRequest{Name: username, Email: email, Password: syntheticPassword, CaptchaToken: "synthetic-captcha", AffiliationSpell: s.Spell}, username)
			if e != nil {
				t.Fatal(e)
			}
			// The ordinary password route must still reject this unverified account.
			if _, e = service.Start(context.Background(), signin.StartRequest{Account: username}, username); !errors.Is(e, signin.ErrUnverified) {
				t.Fatal("unverified registration could sign in")
			}
			// Provision a synthetic canonical root for the existing-contact route;
			// no synthetic contact verification is written into the database.
			pair := pgIssue(t, f.issuer, account.ID)
			// A verified secondary contact cannot replace the primary requirement.
			secondary, e := service.CreateContact(context.Background(), pair.Token, signin.ContactInput{Type: 0, Content: username + "-secondary@example.invalid"})
			if e != nil {
				t.Fatal(e)
			}
			if e = service.SendContactVerification(context.Background(), pair.Token, secondary.ID); e != nil {
				t.Fatal(e)
			}
			if e = service.VerifyEmail(context.Background(), delivery.code(secondary.Content, "verify_contact")); e != nil {
				t.Fatal(e)
			}
			if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_authority_activations WHERE account_id=$1`, account.ID); n != 0 {
				t.Fatal("secondary contact activated account", n)
			}
			if e = service.SendContactVerification(context.Background(), pair.Token, account.ID); e != nil {
				t.Fatal(e)
			}
			code := delivery.code(email, "verify_contact")
			if code == "" {
				t.Fatal("missing primary contact challenge")
			}
			if !expire {
				// Even a trusted transaction adapter cannot substitute a different
				// subject, purpose, generation, contact or challenge hash.
				hash := sha256.Sum256([]byte(code))
				for label, mutate := range map[string]func(*signin.Challenge){
					"subject":    func(c *signin.Challenge) { c.AccountID = f.other },
					"purpose":    func(c *signin.Challenge) { c.Purpose = "reset_password" },
					"generation": func(c *signin.Challenge) { c.Version++ },
					"contact":    func(c *signin.Challenge) { c.ContactID = secondary.ID },
					"hash":       func(c *signin.Challenge) { c.Hash = make([]byte, 32) },
					"consumed":   func(c *signin.Challenge) { c.Completed = true },
				} {
					accepted := errors.New("synthetic substituted proof accepted")
					e = store.Within(context.Background(), account.ID, func(tx signin.Transaction) error {
						c, err := tx.Challenge(hash[:])
						if err != nil {
							return err
						}
						v, err := tx.Credentials()
						if err != nil {
							return err
						}
						now := f.config.Now()
						v.VerifiedAt = &now
						if err = tx.SaveCredentials(v); err != nil {
							return err
						}
						mutate(&c)
						if err = tx.(signin.ContactVerificationCompletion).CompleteContactVerification(c); err != nil {
							return err
						}
						return accepted // always roll the probe back
					})
					if errors.Is(e, accepted) || e == nil {
						t.Fatal(label, "substituted challenge accepted")
					}
					if n := commerceCount(t, f.db, `SELECT count(*) FROM owned_authority_activations WHERE account_id=$1`, account.ID); n != 0 {
						t.Fatal(label, "probe persisted activation", n)
					}
				}
			}
			if expire {
				hash := sha256.Sum256([]byte(code))
				query(t, f.db, `UPDATE owned_authority_challenges SET expires_at=$2 WHERE hash=$1`, hash[:], f.config.Now().Add(2*time.Second))
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				locks := atomicAuthorityFixture{db: f.db, ctx: ctx}
				blocker, pid := atomicBlocker(t, locks, "owned_affiliation_spells", s.ID)
				done := make(chan error, 1)
				go func() { done <- service.VerifyEmail(ctx, code) }()
				waitAtomicBlocked(t, locks, pid, "owned_affiliation_spells")
				f.clock.Add(3)
				if e = blocker.Commit(); e != nil {
					t.Fatal(e)
				}
				if e = <-done; e == nil {
					t.Fatal("contact expiry after activation wait committed")
				}
				for _, q := range []string{`SELECT count(*) FROM owned_authority_credentials WHERE account_id=$1 AND verified_at IS NOT NULL`, `SELECT count(*) FROM owned_authority_activations WHERE account_id=$1`, `SELECT count(*) FROM owned_authority_grants WHERE account_id=$1 AND pattern='posts.create'`} {
					if n := commerceCount(t, f.db, q, account.ID); n != 0 {
						t.Fatal("contact/activation/grant survived rollback", q, n)
					}
				}
				if e = service.SendContactVerification(context.Background(), pair.Token, account.ID); e != nil {
					t.Fatal(e)
				}
				code = delivery.code(email, "verify_contact")
			}
			if e = service.VerifyEmail(context.Background(), code); e != nil {
				t.Fatal(e)
			}
			for _, q := range []string{`SELECT count(*) FROM owned_authority_activations WHERE account_id=$1`, `SELECT count(*) FROM owned_authority_grants WHERE account_id=$1 AND pattern='posts.create'`} {
				if n := commerceCount(t, f.db, q, account.ID); n != 1 {
					t.Fatal("primary contact failed activation", q, n)
				}
			}
			if e = service.VerifyEmail(context.Background(), code); e == nil {
				t.Fatal("primary challenge replay accepted")
			}
			// The completed primary contact now permits a real password login.
			login, e := service.Start(context.Background(), signin.StartRequest{Account: username, DeviceName: "Synthetic contact client"}, username)
			if e != nil {
				t.Fatal(e)
			}
			factors, e := service.ChallengeFactors(context.Background(), login.ID)
			if e != nil {
				t.Fatal(e)
			}
			var factor string
			for _, v := range factors {
				if v.Type == 0 {
					factor = v.ID
				}
			}
			if _, e = service.VerifyFactor(context.Background(), login.ID, factor, syntheticPassword, username); e != nil {
				t.Fatal(e)
			}
			loggedIn, e := f.issuer.ExchangeCode(context.Background(), login.ID)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.verifier.Authorize(context.Background(), loggedIn.Token, "posts.create"); e != nil {
				t.Fatal("completed contact grant unavailable to password session", e)
			}
		})
	}
}

func TestAffiliationRestartAdmissionJSONSurvivesMarkerSerialization(t *testing.T) {
	postgres := []byte(`{"id": "synthetic-result", "recipient_id": "synthetic-account", "created_at": "2026-10-08T00:00:00+00:00"}`)
	type saved struct{ Result json.RawMessage }
	marker, e := json.Marshal(saved{Result: postgres})
	if e != nil {
		t.Fatal(e)
	}
	var loaded saved
	if e = json.Unmarshal(marker, &loaded); e != nil {
		t.Fatal(e)
	}
	if string(postgres) == string(loaded.Result) {
		t.Fatal("regression fixture no longer reproduces marker compaction")
	}
	if !equalAffiliationAdmissionJSON(postgres, loaded.Result) {
		t.Fatal("marker formatting changed durable admission")
	}
	for _, changed := range []string{`{"id":"other-result","recipient_id":"synthetic-account","created_at":"2026-10-08T00:00:00+00:00"}`, `{"id":"synthetic-result","recipient_id":"other-account","created_at":"2026-10-08T00:00:00+00:00"}`, `null`, `{bad}`} {
		if equalAffiliationAdmissionJSON([]byte(changed), loaded.Result) {
			t.Fatal("changed admission accepted", changed)
		}
	}
}
