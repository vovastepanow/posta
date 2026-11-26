package posta

import (
	"testing"
	"posta/internal/jx"
)

func TestPublishingCatalogUserCapabilities(t *testing.T) {
	a:=&App{}
	owner, member:=a.NetworksPublicFor(true),a.NetworksPublicFor(false)
	count:=0
	for key,raw:=range owner {
		entry,ok:=raw.(jx.M)
		if !ok { t.Fatalf("unexpected catalog entry %s",key) }
		if jx.Bool(entry,"publishing_in_scope"){count++}
		if !publishingInScope(key) && jx.Bool(entry,"connect_available"){t.Fatalf("out of scope connection: %s",key)}
		m:=member[key].(jx.M)
		if jx.Str(entry,"status")=="beta" && jx.Bool(m,"connect_available"){t.Fatalf("beta connection available to member: %s",key)}
	}
	if count!=14 {t.Fatalf("publishing scope: %d",count)}
	if !jx.Bool(owner["vk"].(jx.M),"connect_available"){t.Fatal("owner cannot connect VK beta")}
	if !jx.Bool(member["telegram"].(jx.M),"connect_available"){t.Fatal("Telegram unavailable")}
}
