package web

import (
	"io"
	"net/http"

	"posta/internal/jx"
)

func init() {
	get("max_bot_status",func(c *Ctx)(any,error){
		out,err:=c.A.MaxBotPublic(c.R.Context(),c.U.ID)
		if err!=nil { return nil,err }
		out["owner"]=c.A.IsOwner(c.R.Context(),c.U.ID)
		return ok(out),nil
	})
	post("max_bot_configure",func(c *Ctx)(any,error){
		if err:=ownerOnly(c);err!=nil { return nil,err }
		if err:=c.A.MaxBotConfigure(c.R.Context(),c.Str("token"));err!=nil { return nil,err }
		return ok(nil),nil
	})
	post("max_bot_link_start",func(c *Ctx)(any,error){
		out,err:=c.A.MaxBotLinkStart(c.R.Context(),c.U.ID)
		if err!=nil { return nil,err }
		return ok(out),nil
	})
	post("max_bot_link_confirm",func(c *Ctx)(any,error){
		if err:=c.A.MaxBotLinkConfirm(c.R.Context(),c.U.ID,c.Str("token"),c.Str("code"));err!=nil { return nil,err }
		return ok(nil),nil
	})
	post("max_bot_unlink",func(c *Ctx)(any,error){
		if err:=c.A.MaxBotUnlink(c.R.Context(),c.U.ID);err!=nil { return nil,err }
		return ok(nil),nil
	})
}

// Separate from browser actions: MAX has no session cookie or X-Posta header.
// Validate the provider secret before parsing or accepting an update.
func (s *Server) maxBotWebhook(w http.ResponseWriter,r *http.Request) {
	if r.Method!=http.MethodPost { w.Header().Set("Allow","POST");w.WriteHeader(http.StatusMethodNotAllowed);return }
	secret:=r.Header.Get("X-Max-Bot-Api-Secret")
	if !s.A.MaxBotWebhookAuthorized(r.Context(),secret) { w.WriteHeader(http.StatusForbidden);return }
	r.Body=http.MaxBytesReader(w,r.Body,1<<20)
	raw,err:=io.ReadAll(r.Body)
	if err!=nil { w.WriteHeader(http.StatusBadRequest);return }
	if err=s.A.MaxBotHandleWebhook(r.Context(),secret,raw);err!=nil { w.WriteHeader(http.StatusServiceUnavailable);return }
	writeJSON(w,http.StatusOK,jx.M{"ok":true})
}
