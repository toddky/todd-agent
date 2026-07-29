# Shared Simscope helpers for the simscope_* tool scripts.
# Not executable, so the tool registry's --schema discovery skips it.

require 'json'
require 'net/http'
require 'uri'

# ==============================================================================
# CONFIG
# ==============================================================================
# The Simscope server; all endpoints are relative to http://<server>/api/.
SIMSCOPE_SERVER = 'aus-ss:8080'.freeze

# ==============================================================================
# TOKEN
# ==============================================================================
# Read the Simscope API token from ~/.simscope.apitoken, matching tt-curl-simscope.
def get_simscope_token
	token_file = File.join(Dir.home, '.simscope.apitoken')
	raise "no Simscope API token found at #{token_file}" unless File.file?(token_file)
	token = File.read(token_file).strip
	raise "Simscope API token at #{token_file} is empty" if token.empty?
	return token
end

# ==============================================================================
# HTTP
# ==============================================================================
# GET one endpoint relative to /api/, merging params into any existing query string.
# Returns the raw response; call simscope_parse on the body.
def simscope_get(endpoint, token, params = {})
	endpoint = endpoint.sub(%r{\A/api/}, '').sub(%r{\A/}, '')
	# Names commonly contain a literal "+" (e.g. "all+2052551").
	# The server decodes it as a space unless it is percent-encoded first.
	endpoint = endpoint.gsub('+', '%2B')
	uri = URI("http://#{SIMSCOPE_SERVER}/api/#{endpoint}")

	unless params.empty?
		merged = URI.decode_www_form(uri.query || '') + params.to_a
		uri.query = URI.encode_www_form(merged)
	end

	request = Net::HTTP::Get.new(uri)
	request['Cookie'] = "simscope-apitoken=#{token}"
	request['Accept'] = 'application/json'

	http = Net::HTTP.new(uri.host, uri.port)
	response = http.request(request)
	return response
end

# Parse a Simscope JSON body.
# The API signals failure with an {"error": "..."} envelope, not an HTTP status.
def simscope_parse(response)
	data = JSON.parse(response.body)
	if data.is_a?(Hash) && data['error']
		raise "Simscope error: #{data['error']}"
	end
	return data
end
