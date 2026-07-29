# Shared Slack helpers for the slack_* tool scripts.
# Not executable, so the tool registry's --schema discovery skips it.

require 'json'
require 'net/http'
require 'time'
require 'uri'

# ==============================================================================
# TOKEN
# ==============================================================================
# Read the Slack OAuth token from mcp_auth.json.
# Resolved under $XDG_CONFIG_HOME/llm-agent (default $HOME/.config).
def get_slack_token()
	xdg = ENV.fetch('XDG_CONFIG_HOME', File.join(Dir.home, '.config'))
	auth_file = File.join(xdg, 'llm-agent', 'mcp_auth.json')
	data = JSON.parse(File.read(auth_file))
	key = data.keys.find { |candidate| candidate.start_with?('slack|') }
	raise "no Slack token found in #{auth_file}" unless key
	return data[key]['accessToken']
end

# ==============================================================================
# HTTP
# ==============================================================================
def slack_get(endpoint, params, token)
	uri = URI("https://slack.com/api/#{endpoint}")
	uri.query = URI.encode_www_form(params)
	http = Net::HTTP.new(uri.host, uri.port)
	http.use_ssl = true
	request = Net::HTTP::Get.new(uri, 'Authorization' => "Bearer #{token}")
	return JSON.parse(http.request(request).body)
end

def slack_post(endpoint, payload, token)
	uri = URI("https://slack.com/api/#{endpoint}")
	http = Net::HTTP.new(uri.host, uri.port)
	http.use_ssl = true
	request = Net::HTTP::Post.new(uri, 'Authorization' => "Bearer #{token}", 'Content-Type' => 'application/json')
	request.body = payload.to_json
	return JSON.parse(http.request(request).body)
end

# ==============================================================================
# MARKDOWN
# ==============================================================================
# Convert standard/GFM markdown to Slack mrkdwn so a normal body renders instead of showing literal ** and [](url).
# Slack-native tokens (:emoji:, <@U...>, <#C...>, <url|text>) pass through untouched.
def markdown_to_mrkdwn(text)
	# Pull code spans out first so nothing inside them gets rewritten.
	# The \x00 sentinel cannot occur in a Slack message body.
	protected_spans = []
	stash = lambda do |match|
		protected_spans.append(match)
		"\x00#{protected_spans.length - 1}\x00"
	end
	converted = text.gsub(/```.*?```/m, &stash)
	converted = converted.gsub(/`[^`\n]+`/, &stash)

	# Links [text](url) -> <url|text>.
	# Done before emphasis so URL punctuation is safe.
	converted = converted.gsub(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/, '<\2|\1>')

	# Stash bold as \x01...\x01 so the italic pass does not
	# re-match the single asterisks that Slack bold collapses to.
	converted = converted.gsub(/\*\*(.+?)\*\*/m) { "\x01#{Regexp.last_match(1)}\x01" }
	converted = converted.gsub(/__(.+?)__/m) { "\x01#{Regexp.last_match(1)}\x01" }
	# Markdown italic *text* -> Slack italic _text_.
	# Slack's own _text_ is already italic and passes through unchanged.
	converted = converted.gsub(/\*([^*\n]+?)\*/, '_\1_')
	converted = converted.gsub(/\x01(.+?)\x01/m, '*\1*')

	# Strikethrough ~~text~~ -> ~text~ (Slack's single-tilde form).
	converted = converted.gsub(/~~(.+?)~~/m, '~\1~')

	# ATX headers -> a bold line (Slack mrkdwn has no headers).
	converted = converted.gsub(/^\s*\#{1,6}\s+(.+?)\s*$/, '*\1*')

	# Restore the stashed code spans.
	return converted.gsub(/\x00(\d+)\x00/) { protected_spans[Regexp.last_match(1).to_i] }
end

# ==============================================================================
# BLOCKS
# ==============================================================================
# Render a message's Block Kit blocks[] to readable text.
# The flattened text field drops tables, code, and every other non-rich_text block, so reading text alone loses them.

# Wrap text in mrkdwn markers from a rich_text leaf's style hash.
# Order matters: code is innermost so its backticks hug the raw text.
def style_text(text, style)
	return text unless style.is_a?(Hash)
	text = "`#{text}`" if style['code']
	text = "*#{text}*" if style['bold']
	text = "_#{text}_" if style['italic']
	text = "~#{text}~" if style['strike']
	return text
end

# Render one leaf of a rich_text_section (text/link/user/emoji/channel/...).
def render_rich_leaf(leaf)
	case leaf['type']
	when 'text'
		return style_text(leaf['text'].to_s, leaf['style'])
	when 'link'
		label = leaf['text'].to_s
		url = leaf['url'].to_s
		return label.empty? ? url : "#{label} (#{url})"
	when 'user'
		return "<@#{leaf['user_id']}>"
	when 'usergroup'
		return "<!subteam^#{leaf['usergroup_id']}>"
	when 'channel'
		return "<##{leaf['channel_id']}>"
	when 'emoji'
		return ":#{leaf['name']}:"
	when 'broadcast'
		return "@#{leaf['range']}"
	else
		return leaf['text'].to_s
	end
end

# Join one rich_text_section's leaves into a single string.
def render_rich_section(section)
	return (section['elements'] || []).map { |leaf| render_rich_leaf(leaf) }.join
end

# Render a rich_text block's children (sections, lists, code, quotes).
def render_rich_text(block)
	parts = []
	(block['elements'] || []).each do |element|
		case element['type']
		when 'rich_text_section'
			parts.append(render_rich_section(element))
		when 'rich_text_list'
			ordered = element['style'] == 'ordered'
			indent = '  ' * element['indent'].to_i
			(element['elements'] || []).each_with_index do |item, index|
				marker = ordered ? "#{index + 1}." : '-'
				parts.append("#{indent}#{marker} #{render_rich_section(item)}")
			end
		when 'rich_text_preformatted'
			code = (element['elements'] || []).map { |leaf| leaf['text'].to_s }.join
			parts.append("```\n#{code}\n```")
		when 'rich_text_quote'
			quoted = (element['elements'] || []).map { |leaf| render_rich_leaf(leaf) }.join
			quoted.split("\n").each { |line| parts.append("> #{line}") }
		end
	end
	return parts.join("\n")
end

# Pull the string out of a composition text object {type, text}.
def composition_text(node)
	return node['text'].to_s if node.is_a?(Hash)
	return node.to_s
end

# Render a table block as a markdown table; the first row is the header.
def render_table(block)
	rows = block['rows'] || []
	return '' if rows.empty?
	# Each cell is a rich_text block; flatten it to one line and escape pipes.
	rendered = rows.map do |row|
		row.map { |cell| render_rich_text(cell).gsub("\n", ' ').gsub('|', '\|').strip }
	end
	header = rendered.first
	lines = ["| #{header.join(' | ')} |", "| #{header.map { '---' }.join(' | ')} |"]
	rendered.drop(1).each { |cells| lines.append("| #{cells.join(' | ')} |") }
	return lines.join("\n")
end

# Render a section block: its text plus any fields.
def render_section(block)
	parts = []
	parts.append(composition_text(block['text'])) if block['text']
	(block['fields'] || []).each { |field| parts.append(composition_text(field)) }
	return parts.join("\n")
end

# Render a context block: mrkdwn/text run together, images as an [image] tag.
def render_context(block)
	parts = []
	(block['elements'] || []).each do |element|
		case element['type']
		when 'mrkdwn', 'plain_text'
			parts.append(element['text'].to_s)
		when 'image'
			alt = element['alt_text'].to_s
			parts.append(alt.empty? ? '[image]' : "[image: #{alt}]")
		end
	end
	return parts.join(' ')
end

# Dispatch one block to its renderer; unknown block types render to "".
def render_block(block)
	case block['type']
	when 'rich_text'
		return render_rich_text(block)
	when 'section'
		return render_section(block)
	when 'header'
		return "*#{composition_text(block['text'])}*"
	when 'context'
		return render_context(block)
	when 'table'
		return render_table(block)
	when 'divider'
		return '---'
	when 'image'
		title = block['title'] ? composition_text(block['title']) : ''
		alt = block['alt_text'].to_s
		url = block['image_url'].to_s
		label = [title, alt].reject(&:empty?).join(' - ')
		suffix = url.empty? ? '' : " #{url}"
		return label.empty? ? "[image#{suffix}]" : "[image: #{label}#{suffix}]"
	else
		return ''
	end
end

# Render all of a message's blocks, blank line between them.
def render_blocks(blocks)
	return '' unless blocks.is_a?(Array)
	return blocks.map { |block| render_block(block) }.reject(&:empty?).join("\n\n")
end

# Render attachment unfurls (link previews, quoted messages) to text.
def render_attachments(attachments)
	return '' unless attachments.is_a?(Array)
	parts = []
	attachments.each do |attachment|
		lines = []
		title = attachment['title'].to_s
		link = attachment['title_link'].to_s
		lines.append(link.empty? ? title : "#{title} (#{link})") unless title.empty?
		text = attachment['text'].to_s
		lines.append(text) unless text.empty?
		if lines.empty?
			blocks_text = render_blocks(attachment['blocks'])
			lines.append(blocks_text) unless blocks_text.empty?
			lines.append(attachment['fallback'].to_s) if lines.empty? && !attachment['fallback'].to_s.empty?
		end
		parts.append(lines.join("\n")) unless lines.empty?
	end
	return parts.join("\n\n")
end

# Assemble one message's readable body: rendered blocks (falling back to the
# flattened text when there are none) plus any attachment unfurls.
def message_body(message)
	body = render_blocks(message['blocks'])
	body = message['text'].to_s if body.empty?
	attachments = render_attachments(message['attachments'])
	return body if attachments.empty?
	return attachments if body.empty?
	return "#{body}\n\n#{attachments}"
end

# ==============================================================================
# FORMAT
# ==============================================================================
# Local-machine time matches the MCP tool's rendering when the host tz is the
# reader's tz. Format e.g. "2026-07-28 11:17:29 CDT".
def format_ts(timestamp)
	return Time.at(timestamp.to_f).strftime('%Y-%m-%d %H:%M:%S %Z')
end

# Resolve a user_id to [real_name, email], caching each lookup in the passed hash.
def resolve_user(user_id, token, cache)
	return cache[user_id] if cache.key?(user_id)
	info = slack_get('users.info', { 'user' => user_id }, token)
	member = info['user'] || {}
	profile = member['profile'] || {}
	real_name = profile['real_name'].to_s
	real_name = member['real_name'].to_s if real_name.empty?
	real_name = user_id if real_name.empty?
	email = profile['email'].to_s
	cache[user_id] = [real_name, email]
	return cache[user_id]
end

# Build the "Name <email> (ID)" identity string for one message's sender.
def sender_identity(message, token, cache)
	bot_profile = message['bot_profile']
	return "#{bot_profile['name']} [bot]" if bot_profile

	user_id = message['user'].to_s
	if user_id.empty?
		username = message['username'].to_s
		return username.empty? ? 'unknown' : username
	end

	real_name, email = resolve_user(user_id, token, cache)
	return email.empty? ? "#{real_name} (#{user_id})" : "#{real_name} <#{email}> (#{user_id})"
end

# Render a message's reactions as "name (count)  name (count)", or "" if none.
def format_reactions(message)
	reactions = message['reactions'] || []
	return '' if reactions.empty?
	reaction_parts = []
	reactions.each do |reaction|
		reaction_parts.append("#{reaction['name']} (#{reaction['count']})")
	end
	return "Reactions: #{reaction_parts.join('  ')}"
end
