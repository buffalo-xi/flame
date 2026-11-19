import i18n from '@/locales'

const t = (key, params) => i18n.global.t(key, params)
const joinList = (list) => list.join(i18n.global.t('common.listJoiner'))

export function parseArguments(argsStr) {
  if (!argsStr) {
    return {}
  }
  try {
    return JSON.parse(argsStr)
  } catch {
    return {}
  }
}

export function stringifyArguments(args) {
  if (!args || typeof args !== 'object') {
    return ''
  }
  try {
    return JSON.stringify(args, null, 2)
  } catch {
    return ''
  }
}

export function getArgumentValue(args, key, defaultValue = null) {
  if (!args || typeof args !== 'object') {
    return defaultValue
  }
  return args[key] !== undefined ? args[key] : defaultValue
}

export function setArgumentValue(args, key, value) {
  if (!args || typeof args !== 'object') {
    return { [key]: value }
  }
  return {
    ...args,
    [key]: value
  }
}

export function removeArgument(args, key) {
  if (!args || typeof args !== 'object') {
    return {}
  }
  const newArgs = { ...args }
  delete newArgs[key]
  return newArgs
}

export function validateArguments(args, schema) {
  const errors = []
  if (!args) {
    return errors
  }
  Object.keys(schema).forEach(key => {
    const rule = schema[key]
    const value = args[key]
    
    if (rule.required && value === undefined) {
      errors.push(t('flow.argumentError.required', { key }))
      return
    }
    
    if (value !== undefined) {
      if (rule.type && typeof value !== rule.type) {
        errors.push(t('flow.argumentError.type', { key, type: rule.type }))
      }
      
      if (rule.min !== undefined && typeof value === 'number' && value < rule.min) {
        errors.push(t('flow.argumentError.min', { key, min: rule.min }))
      }
      
      if (rule.max !== undefined && typeof value === 'number' && value > rule.max) {
        errors.push(t('flow.argumentError.max', { key, max: rule.max }))
      }
      
      if (rule.minLength !== undefined && typeof value === 'string' && value.length < rule.minLength) {
        errors.push(t('flow.argumentError.minLength', { key, min: rule.minLength }))
      }
      
      if (rule.maxLength !== undefined && typeof value === 'string' && value.length > rule.maxLength) {
        errors.push(t('flow.argumentError.maxLength', { key, max: rule.maxLength }))
      }
      
      if (rule.pattern && typeof value === 'string' && !rule.pattern.test(value)) {
        errors.push(t('flow.argumentError.pattern', { key }))
      }
      
      if (rule.enum && !rule.enum.includes(value)) {
        errors.push(t('flow.argumentError.enum', { key, options: joinList(rule.enum) }))
      }
    }
  })
  return errors
}

export function mergeArguments(target, source) {
  if (!target) {
    return source || {}
  }
  if (!source) {
    return target
  }
  return {
    ...target,
    ...source
  }
}

export function hasArguments(args) {
  return args && typeof args === 'object' && Object.keys(args).length > 0
}

export function getArgumentKeys(args) {
  if (!args || typeof args !== 'object') {
    return []
  }
  return Object.keys(args)
}

export function validateArgumentsSchema(data) {
  if (!data || typeof data !== 'object' || Array.isArray(data)) {
    return [t('flow.argumentError.notObject')]
  }
  if (data.type !== 'object') {
    return [t('flow.argumentError.typeMustBeObject')]
  }
  if (!data.properties || typeof data.properties !== 'object' || Array.isArray(data.properties)) {
    return [t('flow.argumentError.propertiesRequired')]
  }
  
  const errors = []
  
  for (const key of Object.keys(data.properties)) {
    const propertySchema = data.properties[key]
    const propertyErrors = validatePropertySchema(propertySchema, `properties.${key}`)
    errors.push(...propertyErrors)
  }
  
  if (data.required) {
    if (!Array.isArray(data.required)) {
      errors.push(t('flow.argumentError.requiredMustBeArray'))
    } else {
      for (const requiredKey of data.required) {
        if (!data.properties[requiredKey]) {
          errors.push(t('flow.argumentError.requiredMissing', { key: requiredKey }))
        }
      }
    }
  }
  
  return errors.length > 0 ? errors : []
}

function validatePropertySchema(schema, path) {
  const errors = []
  
  if (!schema || typeof schema !== 'object' || Array.isArray(schema)) {
    errors.push(t('flow.argumentError.pathNotObject', { path }))
    return errors
  }
  
  if (!schema.type) {
    errors.push(t('flow.argumentError.missingType', { path }))
    return errors
  }
  
  const validTypes = ['string', 'number', 'array', 'object']
  if (!validTypes.includes(schema.type)) {
    errors.push(t('flow.argumentError.invalidType', { path, types: joinList(validTypes) }))
    return errors
  }
  
  if (schema.type === 'array') {
    if (!schema.items) {
      errors.push(t('flow.argumentError.arrayNeedsItems', { path }))
    } else {
      const itemErrors = validatePropertySchema(schema.items, `${path}.items`)
      errors.push(...itemErrors)
    }
  }  
  if (schema.type === 'object') {
    if (schema.properties) {
      if (typeof schema.properties !== 'object' || Array.isArray(schema.properties)) {
        errors.push(t('flow.argumentError.propertiesNotObject', { path }))
      } else {
        for (const key of Object.keys(schema.properties)) {
          const propertyErrors = validatePropertySchema(schema.properties[key], `${path}.properties.${key}`)
          errors.push(...propertyErrors)
        }
      }
    }    
    if (schema.required) {
      if (!Array.isArray(schema.required)) {
        errors.push(t('flow.argumentError.nestedRequiredMustBeArray', { path }))
      } else {
        for (const requiredKey of schema.required) {
          if (!schema.properties[requiredKey]) {
            errors.push(t('flow.argumentError.nestedRequiredMissing', { path, key: requiredKey }))
          }
        }
      }
    }
  }  
  return errors
}

export function isValidArgumentsSchema(data) {
  const errors = validateArgumentsSchema(data)
  return {
    valid: errors.length === 0,
    errors: errors
  }
}

