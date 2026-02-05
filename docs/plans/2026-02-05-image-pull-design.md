# Design: `devsandbox image pull` Command

## Overview

A manual command to update the Docker image used by devsandbox. Pulls the configured image from the registry and reports whether anything changed.

**Command:** `devsandbox image pull`

**Philosophy:** User controls when updates happen. No automatic pulls except on first use (`pull_policy = "missing"` remains the default).

## Behavior

1. **Determine image** - Read from config chain:
   - Project `.devsandbox.toml` → User `~/.config/devsandbox/config.toml` → Default `ghcr.io/zekker6/devsandbox:latest`

2. **Check current state** - If image exists locally, capture:
   - Image ID (digest)
   - Creation timestamp

3. **Pull image** - Run `docker pull <image>`, stream progress to stdout

4. **Report result:**
   - `"Updated: image was 3 days old"` - if digest changed
   - `"Already up to date"` - if digest unchanged
   - `"Downloaded: image was not present locally"` - if first pull

## Implementation

**Location:** `cmd/devsandbox/image.go` - new file for `image` subcommand group

**Command structure:**
```
devsandbox image
  └── pull    # Pull/update the configured Docker image
```

**Key functions:**

```go
// getImageInfo returns digest and creation time for a local image
func getImageInfo(image string) (*ImageInfo, error)

// pullImage runs docker pull and streams output
func pullImage(image string) error
```

**Output examples:**
```
$ devsandbox image pull
Pulling ghcr.io/zekker6/devsandbox:latest...
[docker pull output streams here]
Updated: image was 3 days old

$ devsandbox image pull
Pulling ghcr.io/zekker6/devsandbox:latest...
Already up to date
```

**Error handling:**
- Docker not available → clear error message
- Network failure → show docker's error, non-zero exit
- Invalid image name → show docker's error

## Testing

**Unit tests (`cmd/devsandbox/image_test.go`):**
- `getImageInfo` returns correct digest/timestamp
- `getImageInfo` returns nil when image doesn't exist
- Correct image selected from config chain

**E2E tests (`e2e/image_test.go`):**
- `devsandbox image pull` succeeds with valid image
- Reports "Already up to date" on second pull
- Works when no local config exists (uses default)

**Manual testing:**
- Pull → verify "Downloaded" message
- Pull again → verify "Already up to date"
- Wait/push new image → verify "Updated: image was X old"

## Future Considerations (not in scope)

- `devsandbox image list` - show downloaded images
- `devsandbox image build` - build from local Dockerfile
- `--check` flag - check for updates without pulling

These can be added later if needed. Keeping initial scope minimal.
