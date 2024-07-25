package core

import (
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
	jsoniter "github.com/json-iterator/go"
	"github.com/yaoapp/kun/log"
	"golang.org/x/net/html"
)

// Load the jit components
var components = map[string]string{}

// TemplateParser parser for the template
type TemplateParser struct {
	data     Data
	mapping  map[string]Mapping                  // variable mapping
	sequence int                                 // sequence for the rendering
	errors   []error                             // errors
	replace  map[*goquery.Selection][]*html.Node // replace nodes
	option   *ParserOption                       // parser option
	locale   *Locale                             // locale
	context  *ParserContext                      // parser context
	scripts  []ScriptNode                        // scripts
	styles   []StyleNode                         // styles
}

// ParserContext parser context for the template
type ParserContext struct {
}

// Mapping mapping for the template
type Mapping struct {
	Key   string      `json:"key,omitempty"`
	Type  string      `json:"type,omitempty"`
	Value interface{} `json:"value,omitempty"`
}

// ParserOption parser option
type ParserOption struct {
	Component    bool    `json:"component,omitempty"`
	Editor       bool    `json:"editor,omitempty"`
	Preview      bool    `json:"preview,omitempty"`
	Debug        bool    `json:"debug,omitempty"`
	DisableCache bool    `json:"disableCache,omitempty"`
	Request      bool    `json:"request,omitempty"`
	Route        string  `json:"route,omitempty"`
	Theme        any     `json:"theme,omitempty"`
	Locale       any     `json:"locale,omitempty"`
	Root         string  `json:"root,omitempty"`
	Script       *Script `json:"-"` // backend script
}

var keepWords = map[string]bool{
	"s:if":        true,
	"s:for":       true,
	"s:for-item":  true,
	"s:for-index": true,
	"s:elif":      true,
	"s:else":      true,
	"s:set":       true,
	"s:bind":      true,
}

var allowUsePropAttrs = map[string]bool{
	"s:if":    true,
	"s:elif":  true,
	"s:for":   true,
	"s:event": true,
}

var keepAttrs = map[string]bool{
	"s:ns":    true,
	"s:cn":    true,
	"s:ready": true,
	"s:event": true,
}

// NewTemplateParser create a new template parser
func NewTemplateParser(data Data, option *ParserOption) *TemplateParser {
	if option == nil {
		option = &ParserOption{}
	}

	return &TemplateParser{
		data:     data,
		mapping:  map[string]Mapping{},
		sequence: 0,
		errors:   []error{},
		replace:  map[*goquery.Selection][]*html.Node{},
		option:   option,
		scripts:  []ScriptNode{},
		styles:   []StyleNode{},
	}
}

// Render parses and renders the HTML template
func (parser *TemplateParser) Render(html string) (string, error) {

	// Set the locale
	parser.locale = parser.Locale()

	if !strings.Contains(html, "<html") {
		html = fmt.Sprintf(`<!DOCTYPE html><html lang="en-us">%s</html>`, html)
	}

	doc, err := NewDocumentString(html)
	if err != nil {
		return "", err
	}

	root := doc.Selection.Find("html")
	parser.parseNode(root.Nodes[0])

	// Replace the nodes
	for sel, nodes := range parser.replace {
		sel.ReplaceWithNodes(nodes...)
		delete(parser.replace, sel)
	}

	// Append the head
	head := doc.Find("head")
	if head.Length() > 0 {

		scriptMessages := map[string]string{}
		if parser.locale != nil && parser.locale.ScriptMessages != nil {
			scriptMessages = parser.locale.ScriptMessages
		}

		data, err := jsoniter.MarshalToString(scriptMessages)
		if err != nil {
			data = "{}"
		}

		head.AppendHtml(headInjectionScript(data))
		parser.addScripts(head, parser.filterScripts("head", parser.scripts))
		parser.addStyles(head, parser.styles)
	}

	// Append the data to the body
	body := doc.Find("body")
	if body.Length() > 0 && !parser.option.Component {
		data, err := jsoniter.MarshalToString(parser.data)
		if err != nil {
			data, _ = jsoniter.MarshalToString(map[string]string{"error": err.Error()})
		}
		body.AppendHtml(bodyInjectionScript(data, parser.debug()))
		parser.addScripts(body, parser.filterScripts("body", parser.scripts))
	}

	// Fmt
	parser.Fmt(doc)

	// For editor
	if parser.option != nil && parser.option.Editor {
		return doc.Find("body").Html()
	}

	// For Request
	if parser.option != nil && (parser.option.Request || parser.option.Preview) {
		// Remove the sui-hide attribute
		doc.Find("[sui-hide]").Remove()
		parser.tidy(doc.Selection)
	}

	// fmt.Println(doc.Html())
	// fmt.Println(parser.errors)
	return doc.Html()
}

// Fmt formats the HTML template
func (parser *TemplateParser) Fmt(doc *goquery.Document) {
	if parser.locale != nil {
		sels := doc.Find(`[s\:trans-fmt]`)
		sels.Each(func(i int, sel *goquery.Selection) {
			name := sel.AttrOr("s:trans-fmt", "")
			sel.SetText(parser.locale.Fmt(name, sel.Text()))
		})
	}
}

// Parse  parses and renders the HTML template
func (parser *TemplateParser) parseNode(node *html.Node) {

	skipChildren := false

	switch node.Type {
	case html.ElementNode:
		sel := goquery.NewDocumentFromNode(node).Selection
		if parser.hasParsed(sel) {
			break
		}
		parser.parseElementNode(sel)

		// Skip children if the node is a loop node
		if _, exist := sel.Attr("s:for"); exist {
			skipChildren = true
		}

	case html.TextNode:
		parser.parseTextNode(node)
	}

	// Recursively process child nodes
	if !skipChildren {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			parser.parseNode(child)
		}
	}
}

func (parser *TemplateParser) parseElementNode(sel *goquery.Selection) {

	parser.transElementNode(sel) // Translations

	node := sel.Get(0)

	if _, exist := sel.Attr("s:for"); exist {
		parser.forStatementNode(sel)
	}

	if _, exist := sel.Attr("s:if"); exist {
		parser.ifStatementNode(sel)
	}

	if _, exist := sel.Attr("s:set"); exist || node.Data == "s:set" {
		parser.setStatementNode(sel)
	}

	// JIT Compile the element
	if parser.isComponent(sel) {
		parser.parseComponent(sel)
	}

	// Parse the attributes
	parser.parseElementAttrs(sel)
}

func (parser *TemplateParser) transTextNode(node *html.Node) {

	parentSel := goquery.NewDocumentFromNode(node.Parent).Selection
	text := strings.TrimSpace(node.Data)
	if text == "" {
		return
	}

	// Translate the node
	if key, exists := parentSel.Attr("s:trans-node"); exists {
		text = parser.transNode(key, text)
	}

	// Escape the text
	if _, exists := parentSel.Attr("s:trans-escape"); exists {
		text = parser.escapeText(text)
	}

	// Translate the text
	if v, exists := parentSel.Attr("s:trans-text"); exists {
		keys := strings.Split(v, ",")
		text = parser.transText(text, keys)
	}

	node.Data = strings.Replace(node.Data, strings.TrimSpace(node.Data), text, 1)
}

func (parser *TemplateParser) transElementNode(sel *goquery.Selection) {

	for _, attr := range sel.Nodes[0].Attr {
		if strings.HasPrefix(attr.Key, "s:trans-attr-") {
			keys := strings.Split(attr.Val, ",")
			name := strings.TrimPrefix(attr.Key, "s:trans-attr-")
			value := sel.AttrOr(name, "")
			if value == "" {
				continue
			}
			newValue := parser.transText(value, keys)
			sel.SetAttr(name, newValue)
		}
	}
}

// 替换转义字符
func (parser *TemplateParser) escapeText(content string) string {
	matches := stmtRe.FindAllStringSubmatch(content, -1)
	newContent := content
	for _, match := range matches {
		text := strings.TrimSpace(match[1])
		newContent = strings.Replace(newContent, text, parser.escape(text), 1)
	}
	return newContent
}

func (parser *TemplateParser) escape(value string) string {
	if strings.HasPrefix(value, "':::") {
		return "'::" + strings.TrimPrefix(value, "':::")
	}

	if strings.HasPrefix(value, "&#39;:::") {
		return "&#39;::" + strings.TrimPrefix(value, "&#39;:::")
	}

	if strings.HasPrefix(value, "\":::") {
		return "\"::" + strings.TrimPrefix(value, "\":::")
	}

	if strings.HasPrefix(value, "&#34;:::") {
		return "&#34;::" + strings.TrimPrefix(value, "&#34;:::")
	}

	return value
}

func (parser *TemplateParser) transNode(key string, message string) string {

	if parser.locale == nil {
		return message
	}

	if lcMessage, has := parser.locale.Keys[key]; has && lcMessage != message {
		return lcMessage
	}

	if lcMessage, has := parser.locale.Messages[message]; has {
		return lcMessage
	}

	return message
}

func (parser *TemplateParser) transText(content string, keys []string) string {

	matches := stmtRe.FindAllStringSubmatch(content, -1)
	newContent := content
	for _, match := range matches {
		text := strings.TrimSpace(match[1])
		if strings.HasPrefix(text, "':::") || strings.HasPrefix(text, "\":::") || strings.HasPrefix(text, "&#39;:::") || strings.HasPrefix(text, "&#34;:::") {
			escaped := parser.escape(text)
			newContent = strings.Replace(newContent, text, escaped, 1)
			continue
		}

		transMatches := transStmtReSingle.FindAllStringSubmatch(text, -1)
		if len(transMatches) == 0 {
			transMatches = transStmtReDouble.FindAllStringSubmatch(text, -1)
		}
		if len(transMatches) > len(keys) {
			return content
		}

		for i, transMatch := range transMatches {
			message := strings.TrimSpace(transMatch[1])

			if parser.locale == nil {
				newContent = strings.Replace(newContent, "::"+message, message, 1)
				continue
			}

			key := keys[i]
			if lcMessage, has := parser.locale.Keys[key]; has && lcMessage != message {
				newContent = strings.Replace(newContent, "::"+message, lcMessage, 1)

				continue
			}

			if lcMessage, has := parser.locale.Messages[message]; has {
				newContent = strings.Replace(newContent, "::"+message, lcMessage, 1)
				continue
			}

			newContent = strings.Replace(newContent, "::"+message, message, 1)
		}
	}
	return newContent
}

// Remove the tag and replace it with the children
func (parser *TemplateParser) removeWrapper(sel *goquery.Selection) {
	children := sel.Children()
	if children.Length() == 0 {
		sel.Remove()
		return
	}
	sel.ReplaceWithSelection(children)
}

func (parser *TemplateParser) setStatementNode(sel *goquery.Selection) {

	sel.SetAttr("parsed", "true")

	name := sel.AttrOr("name", "")
	if name == "" {
		return
	}

	valueExp := sel.AttrOr("value", "")
	if stmtRe.MatchString(valueExp) {
		val, err := parser.data.Exec(valueExp)
		if err != nil {
			log.Warn("Set %s: %s", valueExp, err)
			parser.data[name] = valueExp
			return
		}
		parser.data[name] = val
		return
	}

	parser.data[name] = valueExp
}

func (parser *TemplateParser) parseElementAttrs(sel *goquery.Selection) {
	if len(sel.Nodes) < 0 {
		return
	}

	if sel.AttrOr("parsed", "false") == "true" {
		return
	}

	attrs := sel.Nodes[0].Attr
	for _, attr := range attrs {

		if strings.HasPrefix(attr.Key, "s:attr-") {
			parser.sequence = parser.sequence + 1
			val, _ := parser.data.Exec(attr.Val)
			if v, ok := val.(bool); ok {
				if v {
					sel.SetAttr(strings.TrimPrefix(attr.Key, "s:attr-"), "")
				}
			}
			continue
		}

		// Ignore the s: attributes
		if strings.HasPrefix(attr.Key, "s:") {
			continue
		}

		parser.sequence = parser.sequence + 1
		res, hasStmt := parser.data.Replace(attr.Val)
		if hasStmt {
			bindings := strings.TrimSpace(attr.Val)
			key := fmt.Sprintf("%v", parser.sequence)
			parser.mapping[attr.Key] = Mapping{
				Key:   key,
				Type:  "attr",
				Value: bindings,
			}
			sel.SetAttr(attr.Key, res)
			bindname := fmt.Sprintf("s:bind:%s", attr.Key)
			sel.SetAttr(bindname, bindings)
		}
	}
}

// Check if the element attributes have the s:raw command.
// If true, the sub-node will output the raw data instead of the escaped value.
func checkIsRawElement(node *html.Node) bool {
	if node.Parent != nil && len(node.Parent.Attr) > 0 {
		for _, attr := range node.Parent.Attr {
			if attr.Key == "s:raw" && attr.Val == "true" {
				return true
			}
		}
	}
	return false
}
func (parser *TemplateParser) parseTextNode(node *html.Node) {
	parser.transTextNode(node) // Translations
	parser.sequence = parser.sequence + 1
	res, hasStmt := parser.data.Replace(node.Data)
	// Bind the variable to the parent node
	if node.Parent != nil && hasStmt {
		bindings := strings.TrimSpace(node.Data)
		key := fmt.Sprintf("%v", parser.sequence)
		if bindings != "" {
			if checkIsRawElement(node) {
				node.Type = html.RawNode
			}
			node.Parent.Attr = append(node.Parent.Attr, []html.Attribute{
				{Key: "s:bind", Val: bindings},
				{Key: "s:key-text", Val: key},
			}...)
		}
	}
	node.Data = res
}

func (parser *TemplateParser) forStatementNode(sel *goquery.Selection) {

	parser.sequence = parser.sequence + 1
	parser.setKey("for", sel, parser.sequence)
	parser.parsed(sel)
	parser.hide(sel) // Hide loop node

	forAttr, _ := sel.Attr("s:for")
	forItems, err := parser.data.Exec(forAttr)
	if err != nil {
		parser.errors = append(parser.errors, err)
		return
	}

	items, err := parser.toArray(forItems)
	if err != nil {
		parser.errors = append(parser.errors, err)
		return
	}

	itemVarName := sel.AttrOr("s:for-item", "item")
	indexVarName := sel.AttrOr("s:for-index", "index")
	itemNodes := []*html.Node{}

	// Keep the node if the editor is enabled
	if parser.option.Editor {
		clone := sel.Clone()
		itemNodes = append(itemNodes, clone.Nodes...)
	}

	for idx, item := range items {

		// Create a new node
		new := sel.Clone()
		parser.removeParsed(new)
		parser.data[itemVarName] = item
		parser.data[indexVarName] = idx

		// parser attributes
		// Copy the if Attr from the parent node
		if ifAttr, exists := new.Attr("s:if"); exists {

			res, err := parser.data.Exec(ifAttr)
			if err != nil {
				parser.errors = append(parser.errors, fmt.Errorf("if statement %v error: %v", parser.sequence, err))
				setError(new, err)
				parser.show(new)
				itemNodes = append(itemNodes, new.Nodes...)
				continue
			}

			if res == true {
				parser.hide(new)
				continue
			}
		}

		parser.parseElementAttrs(new)
		parser.parsed(new)

		// Set the key
		parser.sequence = parser.sequence + 1
		parser.setKey("for-item-index", new, idx)
		parser.setKey("for-item-key", new, parser.sequence)

		// Show the node
		parser.show(new)

		if parser.option.Editor {
			parser.setSuiAttr(new, "generate", "true")
		}

		// Process the new node
		for i := range new.Nodes {
			parser.parseNode(new.Nodes[i])
		}
		itemNodes = append(itemNodes, new.Nodes...)
	}

	// Clean up the variables
	delete(parser.data, itemVarName)
	delete(parser.data, indexVarName)

	// Replace the node
	// sel.ReplaceWithNodes(itemNodes...)
	parser.replace[sel] = itemNodes
}

func (parser *TemplateParser) ifStatementNode(sel *goquery.Selection) {

	parser.sequence = parser.sequence + 1
	parser.setKey("if", sel, parser.sequence)
	parser.parsed(sel)
	parser.hide(sel) // Hide all elif and else nodes

	ifAttr, _ := sel.Attr("s:if")
	elifNodes, elseNode := parser.elseStatementNode(sel)

	for _, elifNode := range elifNodes {
		parser.hide(elifNode)
	}

	if elseNode != nil {
		parser.hide(elseNode)
	}

	// show the node if the condition is true
	res, err := parser.data.Exec(ifAttr)
	if err != nil {
		parser.errors = append(parser.errors, fmt.Errorf("if statement %v error: %v", parser.sequence, err))
		return
	}

	if res == true {
		parser.removeParsed(sel)
		parser.parseElementAttrs(sel)
		parser.parsed(sel)
		parser.show(sel)
		return
	}

	// else if
	for _, elifNode := range elifNodes {
		elifAttr := elifNode.AttrOr("s:elif", "")
		res, err := parser.data.Exec(elifAttr)
		if err != nil {
			parser.errors = append(parser.errors, err)
			return
		}

		if res == true {
			parser.removeParsed(elifNode)
			parser.parseElementAttrs(elifNode)
			parser.parsed(elifNode)
			parser.show(elifNode)
			return
		}
	}

	// else
	if elseNode != nil {
		parser.removeParsed(elseNode)
		parser.parseElementAttrs(elseNode)
		parser.parsed(elseNode)
		parser.show(elseNode)
	}
}

func (parser *TemplateParser) elseStatementNode(sel *goquery.Selection) ([]*goquery.Selection, *goquery.Selection) {
	var elseNode *goquery.Selection = nil
	elifNodes := []*goquery.Selection{}
	key := parser.key("if", sel)
	for next := sel.Next(); next != nil; next = next.Next() {
		if _, exist := next.Attr("s:elif"); exist {
			parser.parsed(next)
			parser.setKey("if", next, key)
			elifNodes = append(elifNodes, next)
			continue
		}

		if _, exist := next.Attr("s:else"); exist {
			parser.parsed(next)
			parser.setKey("if", next, key)
			elseNode = next
			continue
		}
		break
	}

	return elifNodes, elseNode
}

func (parser *TemplateParser) setSuiAttr(sel *goquery.Selection, key, value string) *goquery.Selection {
	key = fmt.Sprintf("data-sui-%s", key)
	return sel.SetAttr(key, value)
}

func (parser *TemplateParser) removeSuiAttr(sel *goquery.Selection, key string) *goquery.Selection {
	key = fmt.Sprintf("data-sui-%s", key)
	return sel.RemoveAttr(key)
}

func (parser *TemplateParser) hide(sel *goquery.Selection) {

	if parser.option.Editor {
		parser.setSuiAttr(sel, "hide", "true")
		return
	}

	sel.SetAttr("sui-hide", "true")

	// style := sel.AttrOr("style", "")
	// if strings.Contains(style, "display: none") {
	// 	return
	// }

	// if style != "" {
	// 	style = fmt.Sprintf("%s; display: none", style)
	// } else {
	// 	style = "display: none"
	// }
	// sel.SetAttr("style", style)
}

func (parser *TemplateParser) show(sel *goquery.Selection) {

	if parser.option.Editor {
		parser.removeSuiAttr(sel, "hide")
		return
	}

	sel.RemoveAttr("sui-hide")

	// style := sel.AttrOr("style", "")
	// if !strings.Contains(style, "display: none") {
	// 	return
	// }

	// style = strings.ReplaceAll(style, "display: none", "")
	// if style == "" {
	// 	sel.RemoveAttr("style")
	// 	return
	// }

	// sel.SetAttr("style", style)
}

func (parser *TemplateParser) tidy(s *goquery.Selection) {

	s.Contents().Each(func(i int, child *goquery.Selection) {

		node := child.Get(0)
		if _, exist := child.Attr("s:jit"); node.Data == "slot" || exist {
			parser.tidy(child)
			parser.removeWrapper(child)
			return
		}

		if node.Data == "s:set" {
			child.Remove()
			return
		}

		if node.Type == html.CommentNode {
			child.Remove()
			return
		}

		// Remove the parsed attribute
		attrs := []html.Attribute{}
		for _, attr := range node.Attr {
			if strings.HasPrefix(attr.Key, "s:") && !keepAttrs[attr.Key] {
				continue
			}

			if attr.Key == "parsed" || attr.Key == "is" || strings.HasPrefix(attr.Key, "...") {
				continue
			}
			attrs = append(attrs, attr)
		}

		node.Attr = attrs
		parser.tidy(child)
	})

}

func (parser *TemplateParser) key(prefix string, sel *goquery.Selection) string {
	name := fmt.Sprintf("s:key-%s", prefix)
	return sel.AttrOr(name, "")
}

func (parser *TemplateParser) setKey(prefix string, sel *goquery.Selection, key interface{}) {
	name := fmt.Sprintf("s:key-%s", prefix)
	value := fmt.Sprintf("%v", key)
	sel.SetAttr(name, value)
}

func (parser *TemplateParser) parsed(sel *goquery.Selection) {
	sel.SetAttr("parsed", "true")
}

func (parser *TemplateParser) removeParsed(sel *goquery.Selection) {
	sel.RemoveAttr("parsed")
}

func (parser *TemplateParser) hasParsed(sel *goquery.Selection) bool {
	if parseed, exist := sel.Attr("parsed"); exist && parseed == "true" {
		return true
	}
	return false
}

func (parser *TemplateParser) debug() bool {
	return parser.option != nil && parser.option.Debug
}

func (parser *TemplateParser) disableCache() bool {
	return (parser.option != nil && parser.option.DisableCache) || parser.debug()
}

func (parser *TemplateParser) toArray(value interface{}) ([]interface{}, error) {
	switch values := value.(type) {

	case []interface{}:
		return values, nil

	case []map[string]interface{}:
		res := []interface{}{}
		for _, v := range values {
			res = append(res, v)
		}
		return res, nil

	case nil:
		return []interface{}{}, nil

	case []map[string]string:
		res := []interface{}{}
		for _, v := range values {
			res = append(res, v)
		}
		return res, nil

	case []string:
		res := []interface{}{}
		for _, v := range values {
			res = append(res, v)
		}
		return res, nil

	case []float64:
		res := []interface{}{}
		for _, v := range values {
			res = append(res, v)
		}
		return res, nil

	case []int:
		res := []interface{}{}
		for _, v := range values {
			res = append(res, v)
		}
		return res, nil

	}

	return nil, fmt.Errorf("Cannot convert %v to array", value)
}
