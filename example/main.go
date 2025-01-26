package main

import (
	"embed"
	"fmt"
	"github.com/jasonlovesdoggo/gophertext"
	"io/fs"
)

//go:embed models/literature.gt
var embeddedModels embed.FS

func main() {
	cfg := gophertext.MarkovConfig{
		Order:          3,
		MaxRepeat:      2,
		MinSentenceLen: 5,
		MaxSentenceLen: 25,
		ParagraphBreak: 5,
	}

	model := gophertext.NewMarkovModel(cfg)

	// Training mode
	if false {
		// Load text from file
		text, err := gophertext.LoadHugeTextCorpus("corpus/literature.txt")
		if err != nil {
			panic(err)
		}

		// Train and save
		model.BuildModel(text)
		data, err := model.Save()
		if err != nil {
			panic(err)
		}

		if err := gophertext.SaveModelToFile(data, "models/literature.gt"); err != nil {
			panic(err)
		}
		fmt.Println("Model trained and saved successfully")
		return
	}

	// Generation mode
	//fmt.Println(getAllFilenames(&embeddedModels))
	model, err := gophertext.LoadEmbedded(embeddedModels, "models/literature.gt")
	if err != nil {
		panic(err)
	}

	text, err := model.Generate(100)
	if err != nil {
		panic(err)
	}

	fmt.Println(text, "...")
}

func getAllFilenames(efs *embed.FS) (files []string, err error) {
	if err := fs.WalkDir(efs, ".", func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			return nil
		}

		files = append(files, path)

		return nil
	}); err != nil {
		return nil, err
	}

	return files, nil
}
