package posta

import (
	"path/filepath"
	"strings"
)

// Only known inert/downloadable document types are accepted. The content
// is never rendered as HTML by Posta and is returned as an attachment.
func publishDocumentExtension(name, mime string) string {
	ext:=strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".pdf":
		if mime=="application/pdf"{return ext}
	case ".txt",".csv":
		if strings.HasPrefix(mime,"text/plain"){return ext}
	case ".zip",".docx",".xlsx",".pptx":
		if mime=="application/zip"{return ext}
	}
	return ""
}
