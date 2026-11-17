The two JavaScript files are the `yt-dlp-ejs` solver bundled in the existing
`tools/bin/yt-dlp` executable (version 2026.10.26). They are embedded into the
Go binary so the solver can run offline without downloading scripts at runtime.

SHA-256:

- `lib.min.js`: `c55987fe697e5b9ee18830163f7af85327e9bb5c3e674b969d38c8d205eaa577`
- `core.min.js`: `18da6ce0758b416e7ae645084f4f8801f9f9d59d6c477c05eaa0ff94ebd8cc00`

The solver is distributed under the Unlicense and includes Meriyah (ISC) and
Astring (MIT). The third-party licence notices from the bundled executable are
included in `THIRD_PARTY_LICENSES.txt`.
