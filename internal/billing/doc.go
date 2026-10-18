// Package billing is a small, dependency-free client for the parts of the
// Stripe API that sap13ns hosting needs: Checkout Sessions in subscription
// mode, Customer Portal sessions, Subscriptions, Customers, Prices and
// webhook signature verification.
//
// It talks to the v1 REST API directly over HTTPS with form-encoded request
// bodies and pins the Stripe-Version header to DefaultAPIVersion, so the
// shapes decoded here (for example current_period_end living on subscription
// items, or invoice.parent.subscription_details.subscription) match what
// Stripe returns. Webhook endpoints must be created with the same API version
// and the "snapshot" payload style; see docs/BILLING.md.
//
// A Client is safe for concurrent use. The secret key is never included in
// errors, logs or the String form of the Client.
//
// Typical web-layer flow:
//
//  1. Upgrade button: CreateCheckoutSession with ClientReferenceID and
//     Metadata["site_id"] set to the site, then redirect to session.URL.
//  2. Webhook handler at /billing/webhook: read the body with ReadWebhookBody,
//     call VerifyWebhookWithSecrets, dedupe on Event.ID, then use the typed
//     helpers (Event.CheckoutSession, Event.Subscription, Event.Invoice) and
//     StateFromStatus to update the site's billing row.
//  3. "Manage billing" button: CreatePortalSession and redirect.
package billing
