package main
import("context";"fmt";"os";"posta/internal/config";"posta/internal/db")
func main(){c:=config.LoadServer();d,e:=db.Open(context.Background(),c.PostgresDSN);if e!=nil{fmt.Fprintln(os.Stderr,"database unavailable");os.Exit(1)};defer d.Close();r,e:=d.Pool.Query(context.Background(),`SELECT key,value FROM kv WHERE key LIKE 'classification_budget:%' OR key LIKE 'llm_spent:%' ORDER BY key DESC LIMIT 10`);if e!=nil{panic(e)};defer r.Close();for r.Next(){var key string;var value []byte;if e=r.Scan(&key,&value);e!=nil{panic(e)};fmt.Printf("%s %s\n",key,value)}}
