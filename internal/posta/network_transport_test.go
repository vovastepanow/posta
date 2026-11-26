package posta

import (
	"net"
	"testing"
)

func TestNetworkResolveRejectsUnsafeEndpoints(t *testing.T) {
	lookup := func(string)([]net.IP,error){return []net.IP{net.ParseIP("8.8.8.8")},nil}
	for _, raw := range []string{"http://example.org/a","https://user@example.org/","https://example.org:444/","https://127.0.0.1/","https://169.254.169.254/","https://example.org/#secret"} {
		if _,_,err:=networkResolve(raw,lookup);err==nil{t.Fatalf("unsafe endpoint accepted: %s",raw)}
	}
	u, addrs, err:=networkResolve("https://example.org/path?x=1",lookup)
	if err!=nil || u.Hostname()!="example.org" || len(addrs)!=1 || addrs[0]!="8.8.8.8:443"{t.Fatalf("invalid pinned resolution: %v %v %v",u,addrs,err)}
	_,_,err=networkResolve("https://example.org/",func(string)([]net.IP,error){return []net.IP{net.ParseIP("8.8.8.8"),net.ParseIP("10.0.0.1")},nil})
	if err==nil{t.Fatal("mixed public/private answer accepted")}
}
