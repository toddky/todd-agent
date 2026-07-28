# Shared Glean helpers for the glean_* tool scripts.
# Not executable, so the tool registry's --schema discovery skips it.

require 'json'
require 'net/http'
require 'uri'

# ==============================================================================
# TOKEN
# ==============================================================================
# Read the Glean OAuth entry from mcp_auth.json, resolved under
# `$XDG_CONFIG_HOME/llm-agent` (with the standard `$HOME/.config` default).
def get_glean_auth
	xdg = ENV.fetch('XDG_CONFIG_HOME', File.join(Dir.home, '.config'))
	auth_file = File.join(xdg, 'llm-agent', 'mcp_auth.json')
	data = JSON.parse(File.read(auth_file))
	key = data.keys.find { |candidate| candidate.start_with?('glean|') }
	raise "no Glean token found in #{auth_file}" unless key
	return data[key]
end

# ==============================================================================
# HTTP
# ==============================================================================
# POST to a Glean REST endpoint (e.g. "chat", "getdocuments"). The MCP server
# URL is "https://<host>/mcp/gleen"; the REST API lives on the same host under
# "/rest/api/v1/<endpoint>" with the same OAuth token.
def glean_post(auth, endpoint, payload)
	mcp_uri = URI.parse(auth['serverUrl'])
	rest_uri = URI.parse("https://#{mcp_uri.host}/rest/api/v1/#{endpoint}")
	http = Net::HTTP.new(rest_uri.host, rest_uri.port)
	http.use_ssl = true
	request = Net::HTTP::Post.new(rest_uri, 'Authorization' => "Bearer #{auth['accessToken']}", 'Content-Type' => 'application/json')
	request.body = payload.to_json
	return http.request(request)
end
