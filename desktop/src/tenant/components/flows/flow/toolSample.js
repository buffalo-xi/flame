// Entry-argument samples built from a tool's input_schema.
// Shared by the node palette (prefills a node the user just created from the tools tab), the MCP / RPC properties
// panels (refill when the author switches the tool) and the tool-details dialog, so they never drift apart.

// input_schema arrives either as an object or as a JSON string depending on the connection; normalize it once here.
export const toolSchema = (source) => {
  let schema = source?.input_schema
  if (typeof schema === 'string') {
    try {
      schema = JSON.parse(schema)
    } catch {
      return null
    }
  }
  return schema && typeof schema === 'object' ? schema : null
}

const sampleValue = (def) => {
  if (!def || typeof def !== 'object') return null
  if (Array.isArray(def.enum) && def.enum.length) return def.enum[0]
  switch (def.type) {
    case 'string':
      return ''
    case 'integer':
    case 'number':
      return 0
    case 'boolean':
      return false
    case 'array':
      return []
    case 'object':
      return {}
    default:
      return null
  }
}

// Required fields when the schema names them (the smallest shape that can run), otherwise every top-level property.
// Values are type placeholders only; nested objects stay empty on purpose, so one deep schema cannot flood the editor -
// deleting fields is cheaper than completing a structure.
export const sampleArguments = (source) => {
  const schema = toolSchema(source)
  const properties = schema?.properties
  if (!properties || typeof properties !== 'object') return ''
  const required = Array.isArray(schema.required) ? schema.required.filter(name => properties[name]) : []
  const names = required.length ? required : Object.keys(properties)
  if (!names.length) return ''
  const sample = {}
  for (const name of names) sample[name] = sampleValue(properties[name])
  return JSON.stringify(sample, null, 2)
}
