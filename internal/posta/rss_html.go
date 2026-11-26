package posta

import (
	"strings"
	"strconv"

	xhtml "golang.org/x/net/html"
)

func rssHTMLAttr(n *xhtml.Node,key string)string{
	for _,a:=range n.Attr{if a.Key==key{return strings.TrimSpace(a.Val)}}
	return ""
}

func rssHTMLHidden(n *xhtml.Node)bool{
	if n.Type!=xhtml.ElementNode{return false}
	switch n.Data{
	case "script","style","template","noscript","iframe","object","embed","head":return true
	}
	for _,a:=range n.Attr{if a.Key=="hidden"||a.Key=="aria-hidden"&&strings.EqualFold(a.Val,"true"){return true}}
	return false
}

// Preserve visible paragraph boundaries and literal text, without carrying
// executable markup into the normalized publication.
func rssBodyText(raw string)string{
	if raw==""{return ""}
	doc,err:=xhtml.Parse(strings.NewReader(raw));if err!=nil{return CleanHTMLText(raw)}
	var b strings.Builder
	var walk func(*xhtml.Node)
	walk=func(n *xhtml.Node){
		if rssHTMLHidden(n){return}
		if n.Type==xhtml.TextNode{b.WriteString(n.Data);return}
		block:=false
		if n.Type==xhtml.ElementNode{
			switch n.Data{
			case "br":b.WriteByte('\n');return
			case "p","div","li","section","article","blockquote","h1","h2","h3","h4","h5","h6","pre","tr":block=true
			}
		}
		if block{b.WriteByte('\n')}
		for c:=n.FirstChild;c!=nil;c=c.NextSibling{walk(c)}
		if block{b.WriteByte('\n')}
	}
	walk(doc)
	var lines []string
	blank:=false
	for _,line:=range strings.Split(b.String(),"\n"){
		line=strings.Join(strings.Fields(line)," ")
		if line==""{if len(lines)>0{blank=true};continue}
		if blank{lines=append(lines,"");blank=false}
		lines=append(lines,line)
	}
	return strings.Join(lines,"\n")
}

func rssHTMLAttachments(raw string)[]rssFile{
	if raw==""{return nil}
	doc,err:=xhtml.Parse(strings.NewReader(raw));if err!=nil{return nil}
	var out []rssFile
	var walk func(*xhtml.Node)
	walk=func(n *xhtml.Node){
		if rssHTMLHidden(n){return}
		if n.Type==xhtml.ElementNode{
			switch n.Data{
			case "img":
				w,_:=strconv.Atoi(rssHTMLAttr(n,"width"));h,_:=strconv.Atoi(rssHTMLAttr(n,"height"))
				if w==1||h==1{return}
				raw:=rssHTMLAttr(n,"src")
				for _,key:=range []string{"data-src","data-original","data-lazy-src"}{
					if alt:=rssHTMLAttr(n,key);alt!=""&&(raw==""||strings.HasPrefix(raw,"data:")){raw=alt;break}
				}
				if raw!=""&&!strings.HasPrefix(raw,"data:"){out=append(out,rssFile{URL:raw,Medium:"image"})}
				return
			case "video":
				raw:=rssHTMLAttr(n,"src")
				typ:=rssHTMLAttr(n,"type")
				if raw==""{
					for child:=n.FirstChild;child!=nil;child=child.NextSibling{
						if child.Type!=xhtml.ElementNode||child.Data!="source"{continue}
						src,ct:=rssHTMLAttr(child,"src"),rssHTMLAttr(child,"type")
						if src==""{continue}
						if raw==""{raw,typ=src,ct}
						if ct=="video/mp4"||ct=="video/webm"{raw,typ=src,ct;break}
					}
				}
				if raw!=""{out=append(out,rssFile{URL:raw,Type:typ,Medium:"video",Poster:rssHTMLAttr(n,"poster")})}
				return
			}
		}
		for c:=n.FirstChild;c!=nil;c=c.NextSibling{walk(c)}
	}
	walk(doc)
	return out
}
