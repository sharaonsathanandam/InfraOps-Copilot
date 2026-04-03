package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
)

const (
	actionUpdateBlock     = "UPDATE_BLOCK"
	actionAppendNewModule = "APPEND_NEW_MODULE"
	actionCreateNewToset  = "CREATE_NEW_TOSET_RESOURCE"
	actionAppendToToset   = "APPEND_TO_TOSET"
	actionAppendToModule  = "APPEND_TO_MODULE_MAP"
	opListAppend          = "list_append"
	noBlockLabel          = "none"
)

type Plan struct {
	FilePath         string            `json:"file_path"`
	Action           string            `json:"action"`
	TargetIdentifier *TargetIdentifier `json:"target_identifier,omitempty"`
	Mutations        *Mutation         `json:"mutations,omitempty"`
	NewModuleConfig  *NewModuleConfig  `json:"new_module_config,omitempty"`
	ResourceType     string            `json:"resource_type,omitempty"`
	ResourceName     string            `json:"resource_name,omitempty"`
	Attributes       map[string]string `json:"attributes,omitempty"`
	InitialMember    string            `json:"initial_member,omitempty"`
	NewMember        string            `json:"new_member,omitempty"`
	ModuleName       string            `json:"module_name,omitempty"`
	MapAttribute     string            `json:"map_attribute,omitempty"`
	RoleKey          string            `json:"role_key,omitempty"`
}

type TargetIdentifier struct {
	BlockType  string `json:"block_type"`
	BlockLabel string `json:"block_label"`
}

type Mutation struct {
	Attribute string `json:"attribute"`
	Operation string `json:"operation"`
	Value     string `json:"value"`
}

type NewModuleConfig struct {
	BlockLabel    string `json:"block_label"`
	Source        string `json:"source"`
	Role          string `json:"role"`
	InitialMember string `json:"initial_member"`
}

func main() {
	if len(os.Args) != 2 {
		exitWithError("usage: tf-engine <path-to-plan.json>")
	}

	planPath := os.Args[1]

	plan, err := loadPlan(planPath)
	if err != nil {
		exitWithError(err.Error())
	}

	if err := validatePlan(plan); err != nil {
		exitWithError(err.Error())
	}

	targetTFPath := resolveTargetPath(planPath, plan.FilePath)
	if err := applyPlan(plan, targetTFPath); err != nil {
		exitWithError(err.Error())
	}
}

func loadPlan(planPath string) (*Plan, error) {
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return nil, fmt.Errorf("read plan.json: %w", err)
	}

	var plan Plan
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return nil, fmt.Errorf("decode plan.json: %w", err)
	}

	return &plan, nil
}

func validatePlan(plan *Plan) error {
	if plan.FilePath == "" {
		plan.FilePath = "main.tf"
	}

	switch plan.Action {
	case actionUpdateBlock:
		if plan.TargetIdentifier == nil {
			return errors.New("plan validation failed: target_identifier is required for UPDATE_BLOCK")
		}
		if plan.Mutations == nil {
			return errors.New("plan validation failed: mutations is required for UPDATE_BLOCK")
		}
		if plan.TargetIdentifier.BlockType == "" {
			return errors.New("plan validation failed: target_identifier.block_type is required")
		}
		if plan.Mutations.Attribute == "" {
			return errors.New("plan validation failed: mutations.attribute is required")
		}
		if plan.Mutations.Operation != opListAppend {
			return fmt.Errorf("plan validation failed: unsupported mutations.operation %q", plan.Mutations.Operation)
		}
	case actionAppendNewModule:
		if plan.NewModuleConfig == nil {
			return errors.New("plan validation failed: new_module_config is required for APPEND_NEW_MODULE")
		}
		if plan.NewModuleConfig.BlockLabel == "" {
			return errors.New("plan validation failed: new_module_config.block_label is required")
		}
		if plan.NewModuleConfig.Source == "" {
			return errors.New("plan validation failed: new_module_config.source is required")
		}
		if plan.NewModuleConfig.Role == "" {
			return errors.New("plan validation failed: new_module_config.role is required")
		}
		if plan.NewModuleConfig.InitialMember == "" {
			return errors.New("plan validation failed: new_module_config.initial_member is required")
		}
	case actionCreateNewToset:
		if plan.ResourceType == "" {
			return errors.New("plan validation failed: resource_type is required for CREATE_NEW_TOSET_RESOURCE")
		}
		if plan.ResourceName == "" {
			return errors.New("plan validation failed: resource_name is required for CREATE_NEW_TOSET_RESOURCE")
		}
		if len(plan.Attributes) == 0 {
			return errors.New("plan validation failed: attributes is required for CREATE_NEW_TOSET_RESOURCE")
		}
		if plan.InitialMember == "" {
			return errors.New("plan validation failed: initial_member is required for CREATE_NEW_TOSET_RESOURCE")
		}
	case actionAppendToToset:
		if plan.ResourceType == "" {
			return errors.New("plan validation failed: resource_type is required for APPEND_TO_TOSET")
		}
		if plan.ResourceName == "" {
			return errors.New("plan validation failed: resource_name is required for APPEND_TO_TOSET")
		}
		if plan.NewMember == "" {
			return errors.New("plan validation failed: new_member is required for APPEND_TO_TOSET")
		}
	case actionAppendToModule:
		if plan.ModuleName == "" {
			return errors.New("plan validation failed: module_name is required for APPEND_TO_MODULE_MAP")
		}
		if plan.MapAttribute == "" {
			return errors.New("plan validation failed: map_attribute is required for APPEND_TO_MODULE_MAP")
		}
		if plan.RoleKey == "" {
			return errors.New("plan validation failed: role_key is required for APPEND_TO_MODULE_MAP")
		}
		if plan.NewMember == "" {
			return errors.New("plan validation failed: new_member is required for APPEND_TO_MODULE_MAP")
		}
	default:
		return fmt.Errorf("plan validation failed: unsupported action %q", plan.Action)
	}

	return nil
}

func resolveTargetPath(planPath, filePath string) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}

	return filepath.Join(filepath.Dir(planPath), filePath)
}

func applyPlan(plan *Plan, targetTFPath string) error {
	src, err := os.ReadFile(targetTFPath)
	if err != nil {
		return fmt.Errorf("read target terraform file %q: %w", targetTFPath, err)
	}

	file, diags := hclwrite.ParseConfig(src, targetTFPath, hcl.Pos{Line: 1, Column: 1})
	if diags.HasErrors() {
		return fmt.Errorf("parse terraform file %q: %s", targetTFPath, diags.Error())
	}

	switch plan.Action {
	case actionUpdateBlock:
		targetBlock, err := findTargetBlock(file.Body(), *plan.TargetIdentifier, plan.Mutations.Attribute)
		if err != nil {
			return err
		}

		switch plan.Mutations.Operation {
		case opListAppend:
			if err := appendStringToListAttribute(targetBlock.Body(), plan.Mutations.Attribute, plan.Mutations.Value); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported mutations.operation %q", plan.Mutations.Operation)
		}
	case actionAppendNewModule:
		appendNewModuleBlock(file.Body(), plan.NewModuleConfig)
	case actionCreateNewToset:
		if err := createNewTosetResourceBlock(file.Body(), plan.ResourceType, plan.ResourceName, plan.Attributes, plan.InitialMember); err != nil {
			return err
		}
	case actionAppendToToset:
		targetBlock, err := findResourceBlock(file.Body(), plan.ResourceType, plan.ResourceName)
		if err != nil {
			return err
		}
		if err := appendStringToTosetAttribute(targetBlock.Body(), "for_each", plan.NewMember); err != nil {
			return err
		}
	case actionAppendToModule:
		targetBlock, err := findTargetBlock(file.Body(), TargetIdentifier{
			BlockType:  "module",
			BlockLabel: plan.ModuleName,
		}, plan.MapAttribute)
		if err != nil {
			return err
		}
		if err := appendStringToModuleMapAttribute(targetBlock.Body(), plan.MapAttribute, plan.RoleKey, plan.NewMember); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported action %q", plan.Action)
	}

	formatted := hclwrite.Format(file.Bytes())
	if err := os.WriteFile(targetTFPath, formatted, 0o644); err != nil {
		return fmt.Errorf("write terraform file %q: %w", targetTFPath, err)
	}

	return nil
}

func appendNewModuleBlock(rootBody *hclwrite.Body, config *NewModuleConfig) {
	moduleBlock := hclwrite.NewBlock("module", []string{config.BlockLabel})
	moduleBody := moduleBlock.Body()

	moduleBody.SetAttributeRaw("source", cloneTokens(hclwrite.TokensForValue(cty.StringVal(config.Source))))
	moduleBody.SetAttributeRaw("role", cloneTokens(hclwrite.TokensForValue(cty.StringVal(config.Role))))
	moduleBody.SetAttributeRaw("members", buildListTokens([]hclwrite.Tokens{
		cloneTokens(hclwrite.TokensForValue(cty.StringVal(config.InitialMember))),
	}))

	if len(rootBody.Blocks()) > 0 || len(rootBody.Attributes()) > 0 {
		rootBody.AppendNewline()
		rootBody.AppendNewline()
	}

	rootBody.AppendBlock(moduleBlock)
	rootBody.AppendNewline()
}

func createNewTosetResourceBlock(rootBody *hclwrite.Body, resourceType, resourceName string, attributes map[string]string, initialMember string) error {
	resourceBlock := hclwrite.NewBlock("resource", []string{resourceType, resourceName})
	resourceBody := resourceBlock.Body()

	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		resourceBody.SetAttributeRaw(key, cloneTokens(hclwrite.TokensForValue(cty.StringVal(attributes[key]))))
	}

	resourceBody.SetAttributeRaw("for_each", buildFunctionCallTokens(
		"toset",
		buildListTokens([]hclwrite.Tokens{
			cloneTokens(hclwrite.TokensForValue(cty.StringVal(initialMember))),
		}),
	))
	resourceBody.SetAttributeTraversal("member", hcl.Traversal{
		hcl.TraverseRoot{Name: "each"},
		hcl.TraverseAttr{Name: "value"},
	})

	appendRootBlock(rootBody, resourceBlock)
	return nil
}

func appendRootBlock(rootBody *hclwrite.Body, block *hclwrite.Block) {
	if len(rootBody.Blocks()) > 0 || len(rootBody.Attributes()) > 0 {
		rootBody.AppendNewline()
		rootBody.AppendNewline()
	}

	rootBody.AppendBlock(block)
	rootBody.AppendNewline()
}

func findResourceBlock(body *hclwrite.Body, resourceType, resourceName string) (*hclwrite.Block, error) {
	for _, block := range body.Blocks() {
		if block.Type() != "resource" {
			continue
		}

		labels := block.Labels()
		if len(labels) != 2 {
			continue
		}
		if labels[0] == resourceType && labels[1] == resourceName {
			return block, nil
		}
	}

	return nil, fmt.Errorf("resource block not found: type=%q name=%q", resourceType, resourceName)
}

func findTargetBlock(body *hclwrite.Body, target TargetIdentifier, attributeName string) (*hclwrite.Block, error) {
	var matches []*hclwrite.Block

	for _, block := range body.Blocks() {
		if block.Type() != target.BlockType {
			continue
		}

		labels := block.Labels()
		if isNoLabelTarget(target.BlockLabel) {
			if len(labels) == 0 {
				matches = append(matches, block)
			}
			continue
		}

		if len(labels) == 1 && labels[0] == target.BlockLabel {
			matches = append(matches, block)
		}
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf(
			"target block not found: type=%q label=%q",
			target.BlockType,
			target.BlockLabel,
		)
	}

	if len(matches) == 1 {
		return matches[0], nil
	}

	var attrMatch *hclwrite.Block
	for _, block := range matches {
		if block.Body().GetAttribute(attributeName) == nil {
			continue
		}
		if attrMatch != nil {
			return nil, fmt.Errorf(
				"ambiguous target block: multiple %q blocks already define attribute %q",
				target.BlockType,
				attributeName,
			)
		}
		attrMatch = block
	}

	if attrMatch != nil {
		return attrMatch, nil
	}

	if isNoLabelTarget(target.BlockLabel) && target.BlockType == "locals" {
		return matches[0], nil
	}

	return nil, fmt.Errorf(
		"ambiguous target block: multiple matches for type=%q label=%q",
		target.BlockType,
		target.BlockLabel,
	)
}

func appendStringToListAttribute(body *hclwrite.Body, attrName, value string) error {
	attr := body.GetAttribute(attrName)
	if attr == nil {
		body.SetAttributeValue(attrName, cty.ListVal([]cty.Value{cty.StringVal(value)}))
		return nil
	}

	exprTokens := attr.Expr().BuildTokens(nil)
	existingBytes := exprTokens.Bytes()
	if bytes.Contains(existingBytes, []byte(fmt.Sprintf(`"%s"`, value))) {
		fmt.Println("IDEMPOTENT_SKIP")
		os.Exit(0)
	}

	updatedTokens, err := appendStringToExpression(exprTokens, value)
	if err != nil {
		return fmt.Errorf("attribute %q is not a supported list expression: %w", attrName, err)
	}

	body.SetAttributeRaw(attrName, updatedTokens)

	return nil
}

func appendStringToTosetAttribute(body *hclwrite.Body, attrName, value string) error {
	attr := body.GetAttribute(attrName)
	if attr == nil {
		return fmt.Errorf("attribute %q not found", attrName)
	}

	exprTokens := attr.Expr().BuildTokens(nil)
	existingBytes := exprTokens.Bytes()
	if bytes.Contains(existingBytes, []byte(fmt.Sprintf(`"%s"`, value))) {
		fmt.Println("IDEMPOTENT_SKIP")
		os.Exit(0)
	}

	updatedTokens, err := appendStringToTosetExpression(exprTokens, value)
	if err != nil {
		return fmt.Errorf("attribute %q is not a supported toset expression: %w", attrName, err)
	}

	body.SetAttributeRaw(attrName, updatedTokens)
	return nil
}

func appendStringToTosetExpression(tokens hclwrite.Tokens, value string) (hclwrite.Tokens, error) {
	significant := trimInsignificant(tokens)
	if len(significant) < 4 {
		return nil, errors.New("expression is too short to be a toset(...) call")
	}
	if !isIdentifierToken(significant[0], "toset") {
		return nil, errors.New("expression is not a toset(...) call")
	}
	if !isTokenType(significant[1], hclsyntax.TokenOParen) || !isTokenType(significant[len(significant)-1], hclsyntax.TokenCParen) {
		return nil, errors.New("expression is not a toset(...) call")
	}

	argumentTokens := significant[2 : len(significant)-1]
	listStart, listEnd, err := findSingleInnerListLiteral(argumentTokens)
	if err != nil {
		return nil, fmt.Errorf("unable to locate toset list argument: %w", err)
	}
	if len(trimInsignificant(argumentTokens[:listStart])) != 0 || len(trimInsignificant(argumentTokens[listEnd+1:])) != 0 {
		return nil, errors.New("toset() must wrap exactly one list literal argument")
	}

	elements, err := parseListElements(argumentTokens[listStart : listEnd+1])
	if err != nil {
		return nil, fmt.Errorf("invalid toset list argument: %w", err)
	}

	elements = append(elements, cloneTokens(hclwrite.TokensForValue(cty.StringVal(value))))

	updated := cloneTokens(significant[:2])
	updated = append(updated, buildListTokens(elements)...)
	updated = append(updated, cloneTokens(significant[len(significant)-1:])...)

	return updated, nil
}

type objectEntry struct {
	KeyTokens   hclwrite.Tokens
	ValueTokens hclwrite.Tokens
}

func appendStringToModuleMapAttribute(body *hclwrite.Body, attrName, roleKey, newMember string) error {
	attr := body.GetAttribute(attrName)
	if attr == nil {
		return fmt.Errorf("attribute %q not found", attrName)
	}

	entries, err := parseObjectEntries(attr.Expr().BuildTokens(nil))
	if err != nil {
		return fmt.Errorf("attribute %q is not a supported object expression: %w", attrName, err)
	}

	quotedMember := []byte(fmt.Sprintf(`"%s"`, newMember))
	for idx, entry := range entries {
		if !objectKeyMatches(entry.KeyTokens, roleKey) {
			continue
		}

		if bytes.Contains(entry.ValueTokens.Bytes(), quotedMember) {
			fmt.Println("IDEMPOTENT_SKIP")
			os.Exit(0)
		}

		updatedValueTokens, err := appendStringToExpression(entry.ValueTokens, newMember)
		if err != nil {
			return fmt.Errorf("map key %q does not point to a supported list expression: %w", roleKey, err)
		}

		entries[idx].ValueTokens = updatedValueTokens
		body.SetAttributeRaw(attrName, buildObjectTokens(entries))
		return nil
	}

	entries = append(entries, objectEntry{
		KeyTokens: cloneTokens(hclwrite.TokensForValue(cty.StringVal(roleKey))),
		ValueTokens: buildListTokens([]hclwrite.Tokens{
			cloneTokens(hclwrite.TokensForValue(cty.StringVal(newMember))),
		}),
	})

	body.SetAttributeRaw(attrName, buildObjectTokens(entries))
	return nil
}

func parseObjectEntries(tokens hclwrite.Tokens) ([]objectEntry, error) {
	significant := trimInsignificant(tokens)
	if len(significant) < 2 {
		return nil, errors.New("expression is too short to be an object")
	}
	if !isTokenType(significant[0], hclsyntax.TokenOBrace) || !isTokenType(significant[len(significant)-1], hclsyntax.TokenCBrace) {
		return nil, errors.New("expression is not an object literal")
	}

	inner := significant[1 : len(significant)-1]
	if len(trimInsignificant(inner)) == 0 {
		return nil, nil
	}

	var (
		rawEntries  []hclwrite.Tokens
		current     hclwrite.Tokens
		seenEqual   bool
		seenValue   bool
		brackDepth  int
		braceDepth  int
		parenDepth  int
	)

	for _, token := range inner {
		if seenEqual && seenValue && brackDepth == 0 && braceDepth == 0 && parenDepth == 0 &&
			(isTokenType(token, hclsyntax.TokenNewline) || isTokenType(token, hclsyntax.TokenComma)) {
			trimmed := trimInsignificant(current)
			if len(trimmed) > 0 {
				rawEntries = append(rawEntries, trimmed)
			}
			current = nil
			seenEqual = false
			seenValue = false
			continue
		}

		current = append(current, cloneToken(token))
		if seenEqual && !seenValue && !isInsignificantToken(token) && !isObjectKeyValueSeparator(token) {
			seenValue = true
		}

		switch {
		case isTokenType(token, hclsyntax.TokenOBrack):
			brackDepth++
		case isTokenType(token, hclsyntax.TokenCBrack):
			brackDepth--
		case isTokenType(token, hclsyntax.TokenOBrace):
			braceDepth++
		case isTokenType(token, hclsyntax.TokenCBrace):
			braceDepth--
		case isTokenType(token, hclsyntax.TokenOParen):
			parenDepth++
		case isTokenType(token, hclsyntax.TokenCParen):
			parenDepth--
		case isObjectKeyValueSeparator(token) && brackDepth == 0 && braceDepth == 0 && parenDepth == 0:
			seenEqual = true
		}
	}

	trimmed := trimInsignificant(current)
	if len(trimmed) > 0 {
		rawEntries = append(rawEntries, trimmed)
	}

	entries := make([]objectEntry, 0, len(rawEntries))
	for _, rawEntry := range rawEntries {
		entry, err := splitObjectEntry(rawEntry)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

func splitObjectEntry(tokens hclwrite.Tokens) (objectEntry, error) {
	var (
		brackDepth int
		braceDepth int
		parenDepth int
	)

	for idx, token := range tokens {
		if isObjectKeyValueSeparator(token) && brackDepth == 0 && braceDepth == 0 && parenDepth == 0 {
			keyTokens := trimInsignificant(tokens[:idx])
			valueTokens := trimInsignificant(tokens[idx+1:])
			if len(keyTokens) == 0 {
				return objectEntry{}, errors.New("object entry is missing a key")
			}
			if len(valueTokens) == 0 {
				return objectEntry{}, errors.New("object entry is missing a value")
			}
			return objectEntry{
				KeyTokens:   keyTokens,
				ValueTokens: valueTokens,
			}, nil
		}

		switch {
		case isTokenType(token, hclsyntax.TokenOBrack):
			brackDepth++
		case isTokenType(token, hclsyntax.TokenCBrack):
			brackDepth--
		case isTokenType(token, hclsyntax.TokenOBrace):
			braceDepth++
		case isTokenType(token, hclsyntax.TokenCBrace):
			braceDepth--
		case isTokenType(token, hclsyntax.TokenOParen):
			parenDepth++
		case isTokenType(token, hclsyntax.TokenCParen):
			parenDepth--
		}
	}

	return objectEntry{}, errors.New("object entry is missing a key/value separator")
}

func objectKeyMatches(tokens hclwrite.Tokens, expected string) bool {
	trimmed := trimInsignificant(tokens)
	if len(trimmed) == 0 {
		return false
	}

	keyBytes := trimmed.Bytes()
	quotedExpected := hclwrite.TokensForValue(cty.StringVal(expected)).Bytes()

	return bytes.Equal(keyBytes, quotedExpected) || bytes.Equal(keyBytes, []byte(expected))
}

func buildObjectTokens(entries []objectEntry) hclwrite.Tokens {
	result := hclwrite.Tokens{
		&hclwrite.Token{
			Type:  hclsyntax.TokenOBrace,
			Bytes: []byte("{"),
		},
	}

	for idx, entry := range entries {
		if idx > 0 {
			result = append(result, &hclwrite.Token{
				Type:  hclsyntax.TokenComma,
				Bytes: []byte(","),
			})
		}

		result = append(result, cloneTokens(entry.KeyTokens)...)
		result = append(result, &hclwrite.Token{
			Type:  hclsyntax.TokenEqual,
			Bytes: []byte("="),
		})
		result = append(result, cloneTokens(entry.ValueTokens)...)
	}

	result = append(result, &hclwrite.Token{
		Type:  hclsyntax.TokenCBrace,
		Bytes: []byte("}"),
	})

	return result
}

func appendStringToExpression(tokens hclwrite.Tokens, value string) (hclwrite.Tokens, error) {
	newValueTokens := cloneTokens(hclwrite.TokensForValue(cty.StringVal(value)))

	elements, err := parseListElements(tokens)
	if err == nil {
		elements = append(elements, newValueTokens)
		return buildListTokens(elements), nil
	}

	significant := trimInsignificant(tokens)
	listStart, listEnd, err := findSingleInnerListLiteral(significant)
	if err != nil {
		return nil, err
	}

	elements, err = parseListElements(significant[listStart : listEnd+1])
	if err != nil {
		return nil, err
	}

	elements = append(elements, newValueTokens)

	updated := cloneTokens(significant[:listStart])
	updated = append(updated, buildListTokens(elements)...)
	updated = append(updated, cloneTokens(significant[listEnd+1:])...)

	return updated, nil
}

func parseListElements(tokens hclwrite.Tokens) ([]hclwrite.Tokens, error) {
	significant := trimInsignificant(tokens)
	if len(significant) < 2 {
		return nil, errors.New("expression is too short to be a list")
	}
	if !isTokenType(significant[0], hclsyntax.TokenOBrack) || !isTokenType(significant[len(significant)-1], hclsyntax.TokenCBrack) {
		return nil, errors.New("expression is not a list literal")
	}

	inner := significant[1 : len(significant)-1]
	if len(trimInsignificant(inner)) == 0 {
		return nil, nil
	}

	var (
		elements   []hclwrite.Tokens
		current    hclwrite.Tokens
		brackDepth int
		braceDepth int
		parenDepth int
	)

	for _, token := range inner {
		switch {
		case isTokenType(token, hclsyntax.TokenOBrack):
			brackDepth++
		case isTokenType(token, hclsyntax.TokenCBrack):
			brackDepth--
		case isTokenType(token, hclsyntax.TokenOBrace):
			braceDepth++
		case isTokenType(token, hclsyntax.TokenCBrace):
			braceDepth--
		case isTokenType(token, hclsyntax.TokenOParen):
			parenDepth++
		case isTokenType(token, hclsyntax.TokenCParen):
			parenDepth--
		}

		if isTokenType(token, hclsyntax.TokenComma) && brackDepth == 0 && braceDepth == 0 && parenDepth == 0 {
			trimmed := trimInsignificant(current)
			if len(trimmed) > 0 {
				elements = append(elements, trimmed)
			}
			current = nil
			continue
		}

		current = append(current, cloneToken(token))
	}

	trimmed := trimInsignificant(current)
	if len(trimmed) > 0 {
		elements = append(elements, trimmed)
	}

	return elements, nil
}

func findSingleInnerListLiteral(tokens hclwrite.Tokens) (int, int, error) {
	type listSpan struct {
		start int
		end   int
	}

	var (
		spans        []listSpan
		currentStart = -1
		listDepth    int
	)

	for idx, token := range tokens {
		switch token.Type {
		case hclsyntax.TokenOBrack:
			if listDepth == 0 {
				currentStart = idx
			}
			listDepth++
		case hclsyntax.TokenCBrack:
			if listDepth == 0 {
				return 0, 0, errors.New("unbalanced list tokens")
			}
			listDepth--
			if listDepth == 0 && currentStart >= 0 {
				spans = append(spans, listSpan{start: currentStart, end: idx})
				currentStart = -1
			}
		}
	}

	if listDepth != 0 {
		return 0, 0, errors.New("unbalanced list tokens")
	}
	if len(spans) == 0 {
		return 0, 0, errors.New("expression is not a list literal and does not contain a function-wrapped list literal")
	}
	if len(spans) > 1 {
		return 0, 0, errors.New("expression contains multiple list literals and cannot be updated safely")
	}

	return spans[0].start, spans[0].end, nil
}

func buildListTokens(elements []hclwrite.Tokens) hclwrite.Tokens {
	result := hclwrite.Tokens{
		&hclwrite.Token{
			Type:  hclsyntax.TokenOBrack,
			Bytes: []byte("["),
		},
	}

	for i, element := range elements {
		if i > 0 {
			result = append(result, &hclwrite.Token{
				Type:  hclsyntax.TokenComma,
				Bytes: []byte(","),
			})
		}
		result = append(result, cloneTokens(element)...)
	}

	result = append(result, &hclwrite.Token{
		Type:  hclsyntax.TokenCBrack,
		Bytes: []byte("]"),
	})

	return result
}

func trimInsignificant(tokens hclwrite.Tokens) hclwrite.Tokens {
	start := 0
	for start < len(tokens) && isInsignificantToken(tokens[start]) {
		start++
	}

	end := len(tokens)
	for end > start && isInsignificantToken(tokens[end-1]) {
		end--
	}

	if start >= end {
		return nil
	}

	return cloneTokens(tokens[start:end])
}

func isInsignificantToken(token *hclwrite.Token) bool {
	return token.Type == hclsyntax.TokenNewline || token.Type == hclsyntax.TokenComment
}

func isNoLabelTarget(label string) bool {
	return label == "" || strings.EqualFold(label, noBlockLabel)
}

func isTokenType(token *hclwrite.Token, expected hclsyntax.TokenType) bool {
	return token.Type == expected
}

func isObjectKeyValueSeparator(token *hclwrite.Token) bool {
	return token.Type == hclsyntax.TokenEqual || token.Type == hclsyntax.TokenColon
}

func isIdentifierToken(token *hclwrite.Token, expected string) bool {
	return token.Type == hclsyntax.TokenIdent && string(token.Bytes) == expected
}

func buildFunctionCallTokens(name string, argument hclwrite.Tokens) hclwrite.Tokens {
	result := hclwrite.Tokens{
		&hclwrite.Token{
			Type:  hclsyntax.TokenIdent,
			Bytes: []byte(name),
		},
		&hclwrite.Token{
			Type:  hclsyntax.TokenOParen,
			Bytes: []byte("("),
		},
	}

	result = append(result, cloneTokens(argument)...)
	result = append(result, &hclwrite.Token{
		Type:  hclsyntax.TokenCParen,
		Bytes: []byte(")"),
	})

	return result
}

func cloneTokens(tokens hclwrite.Tokens) hclwrite.Tokens {
	cloned := make(hclwrite.Tokens, 0, len(tokens))
	for _, token := range tokens {
		cloned = append(cloned, cloneToken(token))
	}
	return cloned
}

func cloneToken(token *hclwrite.Token) *hclwrite.Token {
	bytesCopy := make([]byte, len(token.Bytes))
	copy(bytesCopy, token.Bytes)

	return &hclwrite.Token{
		Type:         token.Type,
		Bytes:        bytesCopy,
		SpacesBefore: token.SpacesBefore,
	}
}

func exitWithError(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
