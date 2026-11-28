package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/posta"
)

func inventoryBenchmark(ctx context.Context, cfg config.Server,args []string) error {
	if len(args)<3 || len(args)>4 {
		return fmt.Errorf("inventory-benchmark <query> <legacy-key> <new-directory> [query-vector.json]")
	}
	var vector *posta.InventoryBenchmarkVector
	if len(args)==4 {
		vector=&posta.InventoryBenchmarkVector{}
		if err:=readBenchmarkJSON(args[3],vector);err!=nil{return err}
	}
	ctx,cancel:=context.WithTimeout(ctx,60*time.Second)
	defer cancel()
	d,err:=db.Open(ctx,cfg.PostgresDSN)
	if err!=nil{return err}
	defer d.Close()
	a:=&posta.App{DB:d}
	ranks,samples,err:=a.BuildInventoryBenchmark(ctx,args[0],args[1],vector)
	if err!=nil{return err}
	if err=os.Mkdir(args[2],0700);err!=nil{return err}
	for name,value:=range map[string]any{"ranks.json":ranks,"blind.json":struct{
		Query string `json:"query"`
		Samples []posta.InventoryBenchmarkSample `json:"samples"`
	}{ranks.Query,samples}} {
		b,err:=json.MarshalIndent(value,"","  ")
		if err!=nil{return err}
		if err=os.WriteFile(filepath.Join(args[2],name),b,0600);err!=nil{return err}
	}
	return nil
}

func readBenchmarkJSON(path string, value any) error {
	f,err:=os.Open(path)
	if err!=nil{return err}
	defer f.Close()
	dec:=json.NewDecoder(f)
	dec.DisallowUnknownFields()
	return dec.Decode(value)
}

func scoreInventoryBenchmark(args []string) error {
	if len(args)!=2{return fmt.Errorf("inventory-benchmark-score <ranks.json> <labels.json>")}
	var ranks posta.InventoryBenchmarkRanks
	var labels []posta.InventoryBenchmarkLabel
	if err:=readBenchmarkJSON(args[0],&ranks);err!=nil{return err}
	if err:=readBenchmarkJSON(args[1],&labels);err!=nil{return err}
	scores,err:=posta.ScoreInventoryBenchmark(ranks,labels)
	if err!=nil{return err}
	enc:=json.NewEncoder(os.Stdout)
	enc.SetIndent("","  ")
	return enc.Encode(scores)
}
