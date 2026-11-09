# Contributor instructions

- Write all repository documentation, comments, examples, and diagnostics in US English.
- License original source under MPL-2.0. Preserve dependency licenses and notices, and review license compatibility before adding code or dependencies.
- Keep the Kinsta API client (`internal/kinsta`) standard-library only. Check behavior against the [Kinsta API reference](https://api-docs.kinsta.com) and, where the live API differs from it, follow the live API and say so in a comment.
- Follow Terraform Plugin Framework plan/state consistency, import, and diagnostic conventions. Every resource needs a lifecycle test against the in-memory fake API (`internal/provider/fake_kinsta_test.go`), and the fake must keep mirroring observed live behavior.
- Destructive operations stay guarded: sites and environments keep `deletion_protection = true` by default, and a delete is confirmed by the object being gone, not by the operation status.
- Keep commits atomic and commit completed, verified work frequently. Run `make test` (and `make test-tofu`) before each code commit, and `make generate` when schema, examples or templates change.
- Do not claim live Kinsta verification from local fixtures. Real API tests require explicit opt-in (`TF_ACC`, `KINSTA_TEST_ZONE`) and a zone the tests may write to.
- Follow the Solcreek Gandi provider's OpenTofu Registry release layout. Preserve published version tags and the registered Solcreek signing identity.
