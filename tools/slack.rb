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
def get_slack_token
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
