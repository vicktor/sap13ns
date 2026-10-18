# Billing (Stripe) — operator guide

sap13ns.site offers three hosting plans:

| Plan | Who | Billing |
| --- | --- | --- |
| **Personal** | Non-commercial personal sites | Free. Validated by hand by the operator. **No Stripe involved.** |
| **Pro** | Commercial sites | Stripe subscription (monthly or yearly) |
| **Business** | Larger commercial sites | Stripe subscription (monthly or yearly) |

Paid plans use Stripe Checkout (hosted page) to subscribe, the Stripe Customer
Portal for self-service (card, invoices, plan change, cancellation), and
webhooks to keep each site's billing state in sync. The client code is in
`internal/billing` (standard library only, pinned to Stripe API version
`2026-09-30.endive`).

This guide covers the Stripe-side setup. It is not legal or tax advice.

---

## 1. Accounts and keys

1. Use a **sandbox** (Workbench → Sandboxes) for development and staging, and
   the live account for production. Keys, products, prices, webhook
   endpoints and secrets are separate per environment: repeat the steps below
   in each.
2. In **Workbench → API version**, set the default API version to
   `2026-09-30.endive` (or newer within the same "endive" release). The client
   sends this version on every request; the account default matters for the
   Stripe CLI and for anything created in the Dashboard.
3. Create a **restricted key** (`rk_live_…` / `rk_test_…`) rather than using
   the full secret key. Grant only:
   - Checkout Sessions: write
   - Customer portal: write
   - Subscriptions: write (retrieve, cancel)
   - Customers: read
   - Prices and Products: read

   A full secret key (`sk_…`) also works but grants much more than needed.
   Never commit keys; put them in the systemd `EnvironmentFile`
   (see `internal/config`) with mode `0600`.

## 2. Business and invoice settings

- **Settings → Business**: legal name, tax ID (NIF/VAT), address and support
  email. These appear on receipts and invoices.
- **Settings → Billing → Invoices**: invoice number prefix and footer (for
  example the legal mentions your advisor asks for).
- **Settings → Billing → Subscriptions and emails**: enable customer emails
  for successful payments, failed payments and expiring cards. Enable
  "send a link to the Customer Portal" in failed-payment emails.
- **Billing → Revenue recovery → Retries**: enable Smart Retries over about
  2 weeks, and under "If all retries for a payment fail" choose **cancel the
  subscription**. sap13ns keeps a site online during a 14-day grace period
  after a failed renewal (`SAP13NS_BILLING_GRACE_DAYS`), so the two windows
  should roughly match.

## 3. Products and prices

Create one product per paid plan and one recurring price per billing
interval. Personal has no product.

| Product | Suggested lookup keys | Product metadata |
| --- | --- | --- |
| sap13ns Pro | `sap13ns_pro_monthly`, `sap13ns_pro_yearly` | `plan=pro` |
| sap13ns Business | `sap13ns_business_monthly`, `sap13ns_business_yearly` | `plan=business` |

Dashboard: **Product catalog → Add product**, then add a *recurring* price in
EUR for each interval. Or with the Stripe CLI (amounts in cents; adjust):

```sh
stripe products create -d name="sap13ns Pro" -d "metadata[plan]=pro"
stripe prices create -d product=prod_XXXX -d currency=eur -d unit_amount=1900 \
  -d "recurring[interval]=month" -d lookup_key=sap13ns_pro_monthly \
  -d tax_behavior=inclusive
stripe prices create -d product=prod_XXXX -d currency=eur -d unit_amount=19000 \
  -d "recurring[interval]=year" -d lookup_key=sap13ns_pro_yearly \
  -d tax_behavior=inclusive
```

Notes:

- **Tax behavior** (`inclusive` = price includes VAT, `exclusive` = VAT added
  on top) only matters if you use Stripe Tax, and once set to inclusive or
  exclusive it cannot be changed on that price: create a new price instead.
  Consumer-facing EU prices are usually shown VAT-inclusive.
- If you plan to use Stripe Tax, set the product's **tax code** to the
  software-as-a-service category that fits (Stripe's tax code list has
  separate SaaS entries for business and personal use).
- To change a price later, create a new price, point the env var to it and
  archive the old one. Existing subscribers stay on their old price until you
  migrate them (from the Dashboard or the portal's plan switch).
- The admin price picker (`ListPrices`) lists active recurring prices with
  their product names, so you can copy price IDs from there.

## 4. Customer Portal

**Settings → Billing → Customer portal**:

- Enable: update payment method, billing address, tax ID, invoice history.
- Cancellation: allow, **at the end of the billing period** (the site stays
  online until the period it paid for ends). Optionally collect a reason.
- Subscription update (plan switching): allow switching between the Pro and
  Business prices listed above, with proration as you prefer.
- Default return URL: `https://<portal>/` (sap13ns passes an explicit return
  URL per site anyway).
- Fill in the links to the terms of service and privacy policy.

## 5. Webhook endpoint

sap13ns receives events at **`https://<portal>/billing/webhook`** (for the
public service: `https://portal.sap13ns.site/billing/webhook`, adjust to the
real portal host).

**Workbench → Webhooks → Add destination**:

1. Events from: your account.
2. Payload style: **Snapshot** (thin events are not supported by
   `internal/billing`; it rejects them with an explicit error).
3. API version: **`2026-09-30.endive`** (must match the client so payloads
   decode with the expected shapes).
4. Events:

   | Event | What sap13ns does |
   | --- | --- |
   | `checkout.session.completed` | Links the Stripe customer and subscription to the site (via `client_reference_id` / `metadata[site_id]`). |
   | `customer.subscription.created` | Stores plan, status and period end. |
   | `customer.subscription.updated` | Status changes (trial → active, active → past_due, cancellation scheduled), plan switches, renewals. |
   | `customer.subscription.deleted` | Subscription ended: suspend the site. |
   | `invoice.paid` | Payment succeeded: clears grace / lifts suspension, records the paid period. |
   | `invoice.payment_failed` | Starts or continues the grace period, notifies the owner. |

   Optional: `customer.subscription.paused`, `customer.subscription.resumed`
   (only if you use pausing) and `customer.subscription.trial_will_end`
   (if you offer trials and want a reminder email).
5. Destination type: Webhook endpoint; URL as above.
6. Copy the **signing secret** (`whsec_…`) into `SAP13NS_STRIPE_WEBHOOK_SECRET`.

Equivalent API call (keeps the key out of the shell history by reading it
from the environment):

```sh
curl https://api.stripe.com/v1/webhook_endpoints \
  -u "$SAP13NS_STRIPE_SECRET_KEY:" \
  -d url=https://portal.sap13ns.site/billing/webhook \
  -d api_version=2026-09-30.endive \
  -d "enabled_events[]=checkout.session.completed" \
  -d "enabled_events[]=customer.subscription.created" \
  -d "enabled_events[]=customer.subscription.updated" \
  -d "enabled_events[]=customer.subscription.deleted" \
  -d "enabled_events[]=invoice.paid" \
  -d "enabled_events[]=invoice.payment_failed"
```

(Creating webhook endpoints needs a key with that permission; the restricted
runtime key from step 1 does not need it.)

Other points:

- The endpoint must answer quickly with `2xx`; sap13ns verifies the
  signature, records the event and returns before doing slow work.
  Signature failures return `400`.
- Stripe retries failed deliveries for up to three days. Events can arrive
  out of order or more than once; sap13ns deduplicates by event ID.
- Optionally restrict the route to Stripe's published webhook IP addresses at
  nginx (docs.stripe.com/ips) in addition to signature verification.
- **Rolling the secret**: in the endpoint's menu choose *Roll secret* and keep
  the old one valid for up to 24 hours. Set
  `SAP13NS_STRIPE_WEBHOOK_SECRET="whsec_new,whsec_old"`, restart, and remove
  the old value once it has expired. During the overlap Stripe signs every
  event with both secrets and either one verifies.

## 6. Environment variables

These are the names the web/config layer should read (proposed; the
`internal/billing` package itself takes plain arguments). Billing is disabled
when `SAP13NS_STRIPE_SECRET_KEY` is unset.

| Variable | Required | Example | Meaning |
| --- | --- | --- | --- |
| `SAP13NS_STRIPE_SECRET_KEY` | yes | `rk_live_…` | Restricted (or secret) API key. Test keys (`_test_`) in staging. |
| `SAP13NS_STRIPE_WEBHOOK_SECRET` | yes | `whsec_…` or `whsec_new,whsec_old` | Endpoint signing secret(s), comma-separated during rotation. |
| `SAP13NS_STRIPE_PRICE_PRO` | yes | `price_…` | Pro, monthly. |
| `SAP13NS_STRIPE_PRICE_PRO_YEARLY` | no | `price_…` | Pro, yearly. |
| `SAP13NS_STRIPE_PRICE_BUSINESS` | yes | `price_…` | Business, monthly. |
| `SAP13NS_STRIPE_PRICE_BUSINESS_YEARLY` | no | `price_…` | Business, yearly. |
| `SAP13NS_STRIPE_API_VERSION` | no | `2026-09-30.endive` | Override the pinned API version (keep it equal to the webhook endpoint's). |
| `SAP13NS_STRIPE_AUTOMATIC_TAX` | no | `false` | `true` enables Stripe Tax on Checkout (`automatic_tax[enabled]`). |
| `SAP13NS_STRIPE_TAX_ID_COLLECTION` | no | `true` | Show the business tax ID (VAT number) field in Checkout. |
| `SAP13NS_STRIPE_TRIAL_DAYS` | no | `0` | Free trial length for new subscriptions. |
| `SAP13NS_BILLING_GRACE_DAYS` | no | `14` | Days a site stays online after a failed renewal before suspension. |

The general rule `SAP13NS_STRIPE_PRICE_<PLAN>` / `SAP13NS_STRIPE_PRICE_<PLAN>_YEARLY`
leaves room for more plans later. The service should refuse to start if a
live key is combined with test price IDs or vice versa (prices are
environment-specific, so Checkout would fail anyway).

## 7. What happens to a site

| Stripe subscription status | sap13ns state | Effect |
| --- | --- | --- |
| `active`, `trialing` | active | Site served with the plan's features. |
| `incomplete` | pending | First payment not confirmed yet (3-D Secure, delayed method). Plan not granted yet. Becomes `incomplete_expired` after about 23 h if unpaid. |
| `past_due`, `unpaid`, `paused` | grace | Site stays online, owner sees a billing banner and gets emails. After `SAP13NS_BILLING_GRACE_DAYS` the public site is suspended (content kept, admin still reachable to pay). |
| `canceled`, `incomplete_expired` | canceled | Public site suspended right away; content kept for the retention period in the terms, then deleted after notice. Re-subscribing starts a new Checkout. |

A cancellation from the portal "at period end" keeps the status `active`
(with `cancel_at_period_end=true`) until the paid period ends; Stripe then
sends `customer.subscription.deleted`.

## 8. Testing with the Stripe CLI

```sh
stripe login                      # pick the sandbox
stripe listen \
  --forward-to http://localhost:8137/billing/webhook \
  --events checkout.session.completed,customer.subscription.created,customer.subscription.updated,customer.subscription.deleted,invoice.paid,invoice.payment_failed
```

`stripe listen` prints a signing secret (`whsec_…`) that is different from the
Dashboard endpoint's: put it in `SAP13NS_STRIPE_WEBHOOK_SECRET` of the local
env file and restart sap13ns. The CLI formats events with the account's
default API version, so set that to `2026-09-30.endive` (step 1).

- **Synthetic events**: `stripe trigger checkout.session.completed`,
  `stripe trigger customer.subscription.updated`,
  `stripe trigger invoice.payment_failed`. These create throwaway objects
  without `site_id` metadata, so sap13ns should log them as "unmapped" and
  answer `200`. They are good for checking signature verification and
  routing.
- **End-to-end**: start a real upgrade from the local portal and pay with
  test card `4242 4242 4242 4242` (any future date, any CVC). Use
  `4000 0025 0000 3155` to exercise 3-D Secure and
  `4000 0000 0000 0341` (attaches fine, later charges fail) to simulate a
  failed renewal.
- **Renewals and dunning**: create the customer on a **test clock**
  (Billing → Test clocks) and advance it past the period end to generate
  renewal invoices, failed payments and, eventually, the cancellation.
- **Replays**: `stripe events resend evt_… --webhook-endpoint=we_…` re-sends
  a past event; sap13ns must treat it as a duplicate.

Never use live keys or live webhook secrets on a development machine.

## 9. EU VAT notes (not legal or tax advice)

These are pointers for the conversation with your tax advisor, not
conclusions.

- Website hosting and SaaS sold online are generally treated as
  electronically supplied services for EU VAT purposes. Where VAT is due
  depends mainly on whether the customer is a business (B2B) or a consumer
  (B2C) and where they are established.
- **B2B, customer in another EU country** with a valid VAT number: usually
  the reverse-charge mechanism applies and the invoice shows the customer's
  VAT number and a reverse-charge mention. Enable tax ID collection in
  Checkout (`SAP13NS_STRIPE_TAX_ID_COLLECTION=true`). Stripe checks the
  format at checkout and validates EU VAT numbers against VIES
  asynchronously (`customer.tax_id.updated` reports the result); you remain
  responsible for how unverified numbers are handled.
- **B2C, customer in another EU country**: VAT of the customer's country
  normally applies, typically declared through the One-Stop Shop (OSS). An
  EU-wide threshold for cross-border B2C sales of these services exists below
  which domestic VAT may be charged instead; check whether it applies.
- **Domestic customers** (same country as the operator): domestic VAT
  normally applies to both B2B and B2C. Some territories (for example the
  Canary Islands, Ceuta and Melilla for Spain) are outside the EU VAT area
  and have their own indirect taxes.
- **Customers outside the EU**: EU VAT usually does not apply, but some
  countries require foreign sellers of digital services to register locally.
- Keep evidence of the customer's location (billing address collected by
  Checkout, card country, IP) — `BillingAddressRequired` makes Checkout always
  collect an address.
- **Stripe Tax (optional)** computes the right rate per customer, applies
  reverse charge when a business tax ID is present, and produces reports for
  OSS filings. It has a per-transaction fee and only works after you add your
  tax **registrations** in Dashboard → Tax. Turn it on with
  `SAP13NS_STRIPE_AUTOMATIC_TAX=true` once registrations, product tax codes
  and price tax behavior are set. Without Stripe Tax you would need manual
  tax rates on prices or subscriptions, which `internal/billing` does not
  implement.
- National invoicing rules (numbering, mandatory mentions, e-invoicing or
  invoice-software certification requirements such as Spain's Verifactu)
  may apply to invoices Stripe issues on your behalf. Ask your advisor
  whether Stripe-generated invoices are sufficient or whether invoices must
  also be issued from a certified system.
