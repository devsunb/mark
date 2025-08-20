package macro

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/reconquest/karma-go"
	"github.com/reconquest/pkg/log"
	"github.com/reconquest/regexputil-go"
	"gopkg.in/yaml.v3"

	"github.com/kovetskiy/mark/attachment"
	"github.com/kovetskiy/mark/includes"
)

var reMacroDirective = regexp.MustCompile(
	// <!-- Macro: <regexp>
	//      Template: <template path>
	//      <optional yaml data> -->

	`(?s)` + // dot capture newlines
		/**/ `<!--\s*Macro:\s*(?P<expr>[^\n]+)\n` +
		/*    */ `\s*Template:\s*(?P<template>.+?)\s*` +
		/*   */ `(?P<config>\n.*?)?-->`,
)

type Macro struct {
	Regexp   *regexp.Regexp
	Template *template.Template
	Config   string
}

func (macro *Macro) Apply(
	content []byte,
	attachments []attachment.Attachment,
) ([]byte, error) {
	var err error

	content = macro.Regexp.ReplaceAllFunc(
		content,
		func(match []byte) []byte {
			config := map[string]interface{}{}

			err = yaml.Unmarshal([]byte(macro.Config), &config)
			if err != nil {
				err = karma.Format(
					err,
					"unable to unmarshal macros config template",
				)
			}

			var buffer bytes.Buffer

			err = macro.Template.Execute(&buffer, macro.configure(
				config,
				macro.Regexp.FindSubmatch(match),
				attachments,
			))
			if err != nil {
				err = karma.Format(
					err,
					"unable to execute macros template",
				)
			}

			return buffer.Bytes()
		},
	)

	return content, err
}

func (macro *Macro) configure(node interface{}, groups [][]byte, attachments []attachment.Attachment) interface{} {
	switch node := node.(type) {
	case map[interface{}]interface{}:
		for key, value := range node {
			node[key] = macro.configure(value, groups, attachments)
		}

		return node
	case map[string]interface{}:
		for key, value := range node {
			node[key] = macro.configure(value, groups, attachments)
		}

		// Special handling for ac:image template - auto-populate width/height from attachment
		macro.populateAttachmentDimensions(node, attachments)

		return node
	case []interface{}:
		for key, value := range node {
			node[key] = macro.configure(value, groups, attachments)
		}

		return node
	case string:
		for i, group := range groups {
			node = strings.ReplaceAll(
				node,
				fmt.Sprintf("${%d}", i),
				string(group),
			)
		}

		return node
	}

	return node
}

func ExtractMacros(
	base string,
	includePath string,
	contents []byte,
	templates *template.Template,
) ([]Macro, []byte, error) {
	var err error

	var macros []Macro

	contents = reMacroDirective.ReplaceAllFunc(
		contents,
		func(spec []byte) []byte {
			if err != nil {
				return spec
			}

			groups := reMacroDirective.FindStringSubmatch(string(spec))

			var (
				expr     = regexputil.Subexp(reMacroDirective, groups, "expr")
				template = regexputil.Subexp(
					reMacroDirective,
					groups,
					"template",
				)
				config = regexputil.Subexp(reMacroDirective, groups, "config")
			)

			var macro Macro

			if strings.HasPrefix(template, "#") {
				cfg := map[string]interface{}{}

				err = yaml.Unmarshal([]byte(config), &cfg)
				if err != nil {
					err = karma.Format(
						err,
						"unable to unmarshal macros config template",
					)

					return nil
				}

				body, ok := cfg[template[1:]].(string)
				if !ok {
					err = fmt.Errorf(
						"the template config doesn't have '%s' field",
						template[1:],
					)

					return nil
				}

				macro.Template, err = templates.New(template).Parse(body)
				if err != nil {
					err = karma.Format(
						err,
						"unable to parse template",
					)

					return nil
				}
			} else {
				macro.Template, err = includes.LoadTemplate(base, includePath, template, "{{", "}}", templates)
				if err != nil {
					err = karma.Format(err, "unable to load template")

					return nil
				}
			}

			facts := karma.
				Describe("template", template).
				Describe("expr", expr)

			macro.Regexp, err = regexp.Compile(expr)
			if err != nil {
				err = facts.
					Format(
						err,
						"unable to compile macros regexp",
					)

				return nil
			}

			macro.Config = config

			log.Tracef(
				facts.Describe("config", macro.Config),
				"loaded macro %q",
				expr,
			)

			macros = append(macros, macro)

			return []byte{}
		},
	)

	return macros, contents, err
}

// populateAttachmentDimensions auto-populates Width and Height from attachment metadata
// when they are not specified in the config but an Attachment is specified
func (macro *Macro) populateAttachmentDimensions(config map[string]interface{}, attachments []attachment.Attachment) {
	// Only proceed if this is for an attachment-based template
	attachmentPath, hasAttachment := config["Attachment"].(string)
	if !hasAttachment || attachmentPath == "" {
		return
	}

	// Only populate if Width is not already specified
	if _, hasWidth := config["Width"]; hasWidth {
		return
	}

	// Create a map for fast lookup
	attachmentMap := make(map[string]attachment.Attachment)
	for _, att := range attachments {
		// Store both original name and filename (with slashes replaced)
		attachmentMap[att.Name] = att
		attachmentMap[att.Filename] = att
	}

	// Look up the attachment
	if att, found := attachmentMap[attachmentPath]; found && att.Width != "" {
		config["Width"] = att.Width
		if att.Height != "" {
			config["Height"] = att.Height
		}
	}
}
