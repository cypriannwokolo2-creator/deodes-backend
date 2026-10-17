# Brevo Temporary Sender Runbook (placeholders only — no live secrets here)

Context: OTP/recovery emails send via Brevo (`internal/domain/email`).
Brevo delivers ONLY from sender addresses verified in the Brevo account that
owns the API key. Until `deodes.com` is verified there, sends use the
temporary sender on the verified domain.

## Current (temporary) values — set on the HOST, never in git

| Var | Value |
|---|---|
| `DEODES_BREVO_API_KEY` | the `xkeysib-...` key (server env only) |
| `DEODES_BREVO_FROM_EMAIL` | `admin@codexhange.com` |
| `DEODES_BREVO_FROM_NAME` | `Deodes` |
| `DEODES_BREVO_TEMP_SENDER_NOTICE` | `true` |

With the flag on, every OTP/recovery/backup email carries the footer:
"this email arrives from our temporary sender address (…) while we resolve
a mail-domain issue. This is still Deodes — never share your code."

## Set the key without touching shell history or git

```sh
# Disable history for this session first:
export HISTFILE=
# Paste when prompted (nothing echoed):
read -s BREVO_KEY && export DEODES_BREVO_API_KEY="$BREVO_KEY" && unset BREVO_KEY
export DEODES_BREVO_FROM_EMAIL=admin@codexhange.com
export DEODES_BREVO_FROM_NAME=Deodes
export DEODES_BREVO_TEMP_SENDER_NOTICE=true
# If the API runs under systemd/docker, prefer that system's secret store
# (systemctl set-environment / docker secret / env file chmod 600) so the
# values survive restarts.
```

Then restart the API and watch boot logs:
- `brevo sender verified` + `from=admin@codexhange.com` → good.
- `brevo sender check failed` → the address is not a verified sender in that
  Brevo account: add/verify it under Senders & IPs first.
- `brevo API key unset — OTP emails are skipped` → the env did not reach
  the process; fix the service env, not the code.

## Prove delivery end-to-end

```sh
# 1. Config flag (public, pre-login):
curl -s http://localhost:1100/v1/config | grep -o '"configured":[a-z]*'
# expect: "configured":true under "email"
# 2. Trigger a real code to an address you control, then check inbox+spam.
# 3. Confirm the Brevo messageId in API logs and match it in Brevo dashboard.
```

## Switch-back to Deodes (zero code changes)

1. In Brevo: verify `deodes.com` (SPF `v=spf1 include:spf.brevo.com ~all`,
   the DKIM CNAMEs Brevo shows, DMARC `p=none` to start). Wait for green.
2. Set `DEODES_BREVO_FROM_EMAIL=noreply@deodes.com`,
   `DEODES_BREVO_TEMP_SENDER_NOTICE=false` (key can stay or rotate).
3. Restart, re-run the proof above, confirm the footer is gone.
4. Rotate the temporary key in Brevo (it lived in chat once — treat as
   exposed) and revoke the old one.
