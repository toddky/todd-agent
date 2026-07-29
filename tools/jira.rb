# Shared Jira helpers for the jira_* tool scripts.
# Not executable, so the tool registry's --schema discovery skips it.

require 'json'
require 'net/http'
require 'time'
require 'uri'

# ==============================================================================
# CONFIG
# ==============================================================================
# The REST v2 base; endpoints passed to jira_request are relative to it.
# v2 renders Jira wiki markup in description/comment bodies, no ADF conversion.
JIRA_BASE_URL = 'https://tenstorrent.atlassian.net/rest/api/2'.freeze

# Jira Basic auth is the account email paired with the API token.
JIRA_USER = 'tyamakawa@tenstorrent.com'.freeze

# ==============================================================================
# TOKEN
# ==============================================================================
# Read the Jira API token from ~/.jira.apitoken, matching tt-curl-jira.
def get_jira_token
	token_file = File.join(Dir.home, '.jira.apitoken')
	raise "no Jira API token found at #{token_file}" unless File.file?(token_file)
	return File.read(token_file).strip
end

# ==============================================================================
# HTTP
# ==============================================================================
# Send one request to the REST v2 API. The endpoint is relative to JIRA_BASE_URL;
# a leading /rest/api/2/ or / is stripped so callers can pass either form.
def jira_request(method, endpoint, token, body = nil)
	endpoint = endpoint.sub(%r{\A/rest/api/2/}, '').sub(%r{\A/}, '')
	uri = URI("#{JIRA_BASE_URL}/#{endpoint}")

	request_class = {
		'GET' => Net::HTTP::Get,
		'POST' => Net::HTTP::Post,
		'PUT' => Net::HTTP::Put
	}.fetch(method) { raise "unsupported HTTP method: #{method}" }

	request = request_class.new(uri)
	request.basic_auth(JIRA_USER, token)
	request['Accept'] = 'application/json'
	if body
		request['Content-Type'] = 'application/json'
		request.body = body.is_a?(String) ? body : body.to_json
	end

	http = Net::HTTP.new(uri.host, uri.port)
	http.use_ssl = true
	response = http.request(request)
	return response
end

# ==============================================================================
# FORMAT
# ==============================================================================
# Local-machine time matches Jira's rendering when the host tz is the reader's.
# Jira timestamps look like "2026-07-28T09:17:55.000-0500".
def format_jira_time(timestamp)
	return '' if timestamp.to_s.empty?
	return Time.parse(timestamp).strftime('%Y-%m-%d %H:%M:%S %Z')
rescue ArgumentError
	return timestamp.to_s
end

# Build a "Name <email>" identity from an issue's user object (assignee, etc.).
def jira_user_identity(user)
	return 'unassigned' unless user.is_a?(Hash)
	name = user['displayName'].to_s
	email = user['emailAddress'].to_s
	return name if email.empty?
	return "#{name} <#{email}>"
end
