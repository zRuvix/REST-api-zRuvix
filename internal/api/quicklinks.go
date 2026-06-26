package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"zruvix/internal/presence"
)

const discordCDN = "https://cdn.discordapp.com"

var supportedQuicktypes = map[string]bool{
	"png": true, "gif": true, "webp": true, "jpg": true, "jpeg": true,
}

// handleQuicklink proxies a Discord avatar for /{id}.{ext} requests. Unknown
// extensions fall through to 404.
func handleQuicklink(w http.ResponseWriter, r *http.Request, file string) {
	idx := strings.LastIndex(file, ".")
	if idx < 0 {
		notFound(w)
		return
	}
	userID := file[:idx]
	fileType := file[idx+1:]

	if !supportedQuicktypes[fileType] {
		notFound(w)
		return
	}

	p, err := presence.GetPrettyPresence(userID)
	if err != nil {
		respondError(w, err.HTTPCode, err.Code, err.Message)
		return
	}

	avatar, _ := p.DiscordUser["avatar"].(string)
	discriminator, _ := p.DiscordUser["discriminator"].(string)

	resp, ferr := fetchAvatar(userID, avatar, discriminator, fileType, p.DiscordUser["avatar"] == nil)
	if ferr != nil || resp == nil {
		notFound(w)
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func fetchAvatar(id, avatar, discriminator, fileType string, avatarNil bool) (*http.Response, error) {
	var url string
	switch {
	case !avatarNil && avatar != "":
		ft := fileType
		if !strings.HasPrefix(avatar, "a_") && ft == "gif" {
			ft = "jpg"
		}
		url = fmt.Sprintf("%s/avatars/%s/%s.%s?size=1024", discordCDN, id, avatar, ft)
	case discriminator == "0":
		// New username system: (id >> 22) % 6
		if snowflake, perr := strconv.ParseUint(id, 10, 64); perr == nil {
			url = fmt.Sprintf("%s/embed/avatars/%d.png", discordCDN, (snowflake>>22)%6)
		}
	default:
		// Legacy: discriminator % 5
		if disc, perr := strconv.Atoi(discriminator); perr == nil {
			url = fmt.Sprintf("%s/embed/avatars/%d.png", discordCDN, disc%5)
		}
	}

	if url == "" {
		return nil, fmt.Errorf("could not construct avatar url")
	}
	return http.Get(url)
}

// handleBannerQuicklink proxies a Discord banner for /banner/{id}.{ext}.
func handleBannerQuicklink(w http.ResponseWriter, r *http.Request, file string) {
	idx := strings.LastIndex(file, ".")
	if idx < 0 {
		notFound(w)
		return
	}
	userID := file[:idx]
	fileType := file[idx+1:]

	if !supportedQuicktypes[fileType] {
		notFound(w)
		return
	}

	p, err := presence.GetPrettyPresence(userID)
	if err != nil {
		respondError(w, err.HTTPCode, err.Code, err.Message)
		return
	}

	if p.BannerURL == nil || *p.BannerURL == "" {
		respondError(w, http.StatusNotFound, "no_banner", "User has no banner set")
		return
	}

	// Use the banner hash from the presence data
	banner := ""
	if p.Banner != nil {
		banner = *p.Banner
	}
	if banner == "" {
		notFound(w)
		return
	}
	ft := fileType
	if !strings.HasPrefix(banner, "a_") && ft == "gif" {
		ft = "png"
	}
	url := fmt.Sprintf("%s/banners/%s/%s.%s?size=1024", discordCDN, userID, banner, ft)

	resp, ferr := http.Get(url)
	if ferr != nil || resp == nil {
		notFound(w)
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
