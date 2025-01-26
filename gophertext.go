package gophertext

import (
	"bytes"
	"embed"
	"encoding/gob"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// MarkovConfig holds model configuration
type MarkovConfig struct {
	Order          int    // Markov chain order (2-4 recommended)
	MaxRepeat      int    // Maximum consecutive repeats of same word
	MinSentenceLen int    // Minimum words per sentence
	MaxSentenceLen int    // Maximum words per sentence
	ParagraphBreak int    // Sentences per paragraph
	StopTokens     string // Sentence-ending punctuation
}

type MarkovModel struct {
	config MarkovConfig
	chain  map[string][]string
	mu     sync.RWMutex
	rules  generationRules
	pool   sync.Pool // For prefix buffer reuse
}

type generationRules struct {
	forbiddenSequences map[string]bool
	alwaysCapitalize   map[string]bool
}

// NewMarkovModel creates a new text generator
func NewMarkovModel(cfg MarkovConfig) *MarkovModel {
	if cfg.Order < 1 {
		cfg.Order = 2
	}
	if cfg.StopTokens == "" {
		cfg.StopTokens = ".!?"
	}

	rand.Seed(time.Now().UnixNano())

	return &MarkovModel{
		config: cfg,
		chain:  make(map[string][]string),
		rules: generationRules{
			forbiddenSequences: make(map[string]bool),
			alwaysCapitalize:   make(map[string]bool),
		},
		pool: sync.Pool{
			New: func() interface{} {
				buf := make([]string, 0, cfg.Order*2)
				return &buf
			},
		},
	}
}

// BuildModel processes text and builds the Markov chain
func (m *MarkovModel) BuildModel(text string) {
	text = normalizeText(text)
	words := strings.Fields(text)
	total := len(words)
	chunkSize := 4096

	var wg sync.WaitGroup
	for i := 0; i < total-m.config.Order; i += chunkSize {
		end := i + chunkSize + m.config.Order
		if end > total {
			end = total
		}

		wg.Add(1)
		go func(chunk []string) {
			defer wg.Done()
			localChain := make(map[string][]string)

			for i := 0; i < len(chunk)-m.config.Order; i++ {
				prefix := strings.Join(chunk[i:i+m.config.Order], " ")
				suffix := chunk[i+m.config.Order]
				localChain[prefix] = append(localChain[prefix], suffix)
			}

			m.mu.Lock()
			for k, v := range localChain {
				m.chain[k] = append(m.chain[k], v...)
			}
			m.mu.Unlock()
		}(words[i:end])
	}
	wg.Wait()
}

// Generate outputs words once the model has been trained
func (m *MarkovModel) Generate(wordCount int) (string, error) {
	if len(m.chain) == 0 {
		return "", fmt.Errorf("model not trained")
	}

	var result strings.Builder
	result.Grow(wordCount * 6)

	currentPrefix := m.randomPrefix()
	words := strings.Fields(currentPrefix)
	result.WriteString(currentPrefix)

	// Normalize initial prefix for tracking
	prefixBuffer := make([]string, 0, m.config.Order*2)
	prefixBuffer = append(prefixBuffer, strings.ToLower(currentPrefix))

	wordsGenerated := len(words)
	sentenceCount := 0
	paragraphCount := 0
	lastWord := ""
	repeatCount := 0

	for wordsGenerated < wordCount {
		// Get next word using normalized prefix
		normalizedPrefix := strings.Join(prefixBuffer, " ")
		possible := m.chain[normalizedPrefix]

		if len(possible) == 0 {
			// Fallback to random prefix
			currentPrefix = m.randomPrefix()
			prefixBuffer = strings.Fields(strings.ToLower(currentPrefix))
			possible = m.chain[currentPrefix]
			if len(possible) == 0 {
				return "", fmt.Errorf("broken chain")
			}
		}

		nextWord := possible[rand.Intn(len(possible))]

		// Apply rules and get display version
		displayWord := m.applyGenerationRules(nextWord, &words, &result,
			&sentenceCount, &paragraphCount, &lastWord, &repeatCount)

		// Update tracking buffers
		words = append(words, displayWord)
		prefixBuffer = append(prefixBuffer, strings.ToLower(nextWord))
		if len(prefixBuffer) > m.config.Order {
			prefixBuffer = prefixBuffer[1:]
		}

		// Write to result with space
		if wordsGenerated > 0 {
			result.WriteByte(' ')
		}
		result.WriteString(displayWord)
		wordsGenerated++
	}

	return postProcessText(result.String()), nil
}

// Update applyGenerationRules to track sentence length
func (m *MarkovModel) applyGenerationRules(nextWord string, words *[]string, result *strings.Builder,
	sentenceCount, paragraphCount *int, lastWord *string, repeatCount *int) string {

	// Track sentence length
	*sentenceCount++

	// Rule 1: Prevent word repetition
	if nextWord == *lastWord {
		*repeatCount++
		if *repeatCount > m.config.MaxRepeat {
			return (*words)[rand.Intn(len(*words))]
		}
	} else {
		*repeatCount = 0
	}
	*lastWord = nextWord

	// Rule 2: Enforce sentence length
	if *sentenceCount >= m.config.MaxSentenceLen {
		result.WriteString(". ")
		*sentenceCount = 0
		*paragraphCount++

		// Add paragraph break
		if *paragraphCount%m.config.ParagraphBreak == 0 {
			result.WriteString("\n\n")
		}

		// Capitalize next word
		return strings.Title(nextWord)
	}

	return nextWord
}

// Update postProcessText to remove redundant formatting
func postProcessText(text string) string {
	// Simple cleanup instead of sentence splitting
	return strings.Join(strings.Fields(text), " ")
}

func (m *MarkovModel) Save() ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(struct {
		Config MarkovConfig
		Chain  map[string][]string
	}{
		Config: m.config,
		Chain:  m.chain,
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (m *MarkovModel) Load(data []byte) error {
	var container struct {
		Config MarkovConfig
		Chain  map[string][]string
	}

	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&container); err != nil {
		return err
	}

	m.config = container.Config
	m.chain = container.Chain
	return nil
}

// LoadEmbedded adds embedded model support
func LoadEmbedded(fs embed.FS, path string) (*MarkovModel, error) {
	fmt.Println(os.Getwd())
	data, err := fs.ReadFile(path)
	if err != nil {
		return nil, err
	}

	model := NewMarkovModel(MarkovConfig{})
	return model, model.Load(data)
}

// Text normalization and post-processing
func normalizeText(text string) string {
	// Remove diacritics and normalize text
	t := transform.Chain(norm.NFD, transform.RemoveFunc(func(r rune) bool {
		return unicode.Is(unicode.Mn, r) // Mn: nonspacing marks
	}), norm.NFC)

	result, _, _ := transform.String(t, text)
	return strings.ToLower(result)
}

// Helper methods
func (m *MarkovModel) randomPrefix() string {
	prefixes := make([]string, 0, len(m.chain))
	for k := range m.chain {
		prefixes = append(prefixes, k)
	}
	return prefixes[rand.Intn(len(prefixes))]
}

// SaveModelToFile saves the trained model to disk
func SaveModelToFile(data []byte, filename string) error {
	// Create directory if needed
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	// Text normalization and post-processing

	// Write file with atomic replace
	tmpFile := filename + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write model file: %w", err)
	}

	return os.Rename(tmpFile, filename)
}

// LoadHugeTextCorpus loads text from a .txt file (supports large files)
func LoadHugeTextCorpus(filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", fmt.Errorf("failed to open corpus file: %w", err)
	}
	defer file.Close()

	var result strings.Builder
	result.Grow(1 << 28) // Pre-allocate 256MB buffer

	// Use buffered reading for large files
	buf := make([]byte, 1024*1024) // 1MB buffer
	for {
		n, err := file.Read(buf)
		if n > 0 {
			result.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("error reading corpus file: %w", err)
		}
	}

	return result.String(), nil
}

// LoadTextDir loads multiple .txt files from a directory
func LoadTextDir(dir string) (string, error) {
	var corpus strings.Builder
	files, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("failed to read directory: %w", err)
	}

	for _, f := range files {
		if filepath.Ext(f.Name()) == ".txt" {
			content, err := LoadHugeTextCorpus(filepath.Join(dir, f.Name()))
			if err != nil {
				return "", err
			}
			corpus.WriteString(content)
			corpus.WriteString("\n")
		}
	}

	return corpus.String(), nil
}
