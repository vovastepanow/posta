package posta

import (
	"encoding/json"
	"html"
	"strings"
)

type jsonFeedAuthor struct {
	Name string `json:"name"`
}
type jsonFeedAttachment struct {
	URL string `json:"url"`
	Mime string `json:"mime_type"`
	Title string `json:"title"`
}
type jsonFeedEntry struct {
	ID string `json:"id"`
	URL string `json:"url"`
	Title string `json:"title"`
	ContentHTML *string `json:"content_html"`
	ContentText *string `json:"content_text"`
	Summary string `json:"summary"`
	Image string `json:"image"`
	Published string `json:"date_published"`
	Modified string `json:"date_modified"`
	Authors []jsonFeedAuthor `json:"authors"`
	Author jsonFeedAuthor `json:"author"`
	Attachments []jsonFeedAttachment `json:"attachments"`
}
type jsonFeedDocument struct {
	Version string `json:"version"`
	Title string `json:"title"`
	Description string `json:"description"`
	Authors []jsonFeedAuthor `json:"authors"`
	Author jsonFeedAuthor `json:"author"`
	Items []jsonFeedEntry `json:"items"`
}

// Adapt JSON Feed 1/1.1 into the same feed tree used by the existing
// source discovery, preview, review and delivery pipeline. Do not treat
// arbitrary JSON APIs, error envelopes or missing items as an empty feed.
func jsonFeedTree(data []byte) *xnode {
	var doc jsonFeedDocument
	if len(data)>5<<20 || json.Unmarshal(data,&doc)!=nil || doc.Items==nil || strings.TrimSpace(doc.Title)=="" {
		return nil
	}
	if doc.Version!="https://jsonfeed.org/version/1" && doc.Version!="https://jsonfeed.org/version/1.1" {return nil}
	node:=func(name,value string)*xnode{return &xnode{N:name,T:value}}
	author:=func(authors []jsonFeedAuthor,old jsonFeedAuthor)string{
		var names []string
		for _,a:=range authors {if a.Name!=""{names=append(names,a.Name)}}
		if len(names)>0{return strings.Join(names,", ")}
		return old.Name
	}
	root:=&xnode{N:"rss"}
	channel:=&xnode{N:"channel",K:[]*xnode{node("title",html.EscapeString(doc.Title)),node("description",html.EscapeString(doc.Description))}}
	root.K=append(root.K,channel)
	ids:=map[string]bool{}
	for _,entry:=range doc.Items{
		if strings.TrimSpace(entry.ID)=="" || ids[entry.ID] || (entry.ContentHTML==nil&&entry.ContentText==nil) {return nil}
		ids[entry.ID]=true
		if len(channel.K)>=rssMaxItems+2{continue}
		item:=&xnode{N:"item",A:map[string]string{"json_feed":"true"}}
		guid:=node("guid",entry.ID);guid.A=map[string]string{"isPermaLink":"false"}
		item.K=append(item.K,guid,node("link",entry.URL),node("title",html.EscapeString(entry.Title)),node("pubDate",entry.Published),node("updated",entry.Modified))
		if entry.ContentHTML!=nil && strings.TrimSpace(*entry.ContentHTML)!="" {
			item.K=append(item.K,node("content:encoded",*entry.ContentHTML))
		}else if entry.ContentText!=nil{
			item.K=append(item.K,node("content:encoded",html.EscapeString(*entry.ContentText)))
		}
		if entry.Summary!=""{item.K=append(item.K,node("description",html.EscapeString(entry.Summary)))}
		name:=author(entry.Authors,entry.Author)
		if name==""{name=author(doc.Authors,doc.Author)}
		if name!=""{item.K=append(item.K,node("author",name))}
		// Same nonempty title denotes alternative encodings, not separate
		// attachments (JSON Feed 1.1). Prefer supported video/image formats.
		selected:=map[string]int{}
		var attachments []jsonFeedAttachment
		rank:=func(a jsonFeedAttachment)int{switch strings.ToLower(a.Mime){case "video/mp4":return 3;case "video/webm":return 2};if strings.HasPrefix(strings.ToLower(a.Mime),"image/"){return 2};return 1}
		for _,a:=range entry.Attachments{
			if a.URL==""||a.Mime==""{return nil}
			if a.Title!=""{if i,ok:=selected[a.Title];ok{if rank(a)>rank(attachments[i]){attachments[i]=a};continue};selected[a.Title]=len(attachments)}
			attachments=append(attachments,a)
		}
		for _,a:=range attachments{item.K=append(item.K,&xnode{N:"enclosure",A:map[string]string{"url":a.URL,"type":a.Mime}})}
		if entry.Image!=""{
			item.K=append(item.K,&xnode{N:"media:thumbnail",A:map[string]string{"url":entry.Image}})
		}
		channel.K=append(channel.K,item)
	}
	return root
}
