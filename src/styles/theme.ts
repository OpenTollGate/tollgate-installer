// Theme configuration for the TollGate Installer
// These colors should match the TollGate captive portal style

export const theme = {
  colors: {
    // Primary colors
    primary: '#f7b44c', // Changed to gold color
    primaryDark: '#FFC400',
    primaryLight: '#FFF2CC',
    
    // Text colors
    text: '#293241', // Darker navy for main text
    textSecondary: '#6B7280', // Medium gray for secondary text
    textLight: '#FFFFFF', // White text for buttons, etc.
    
    // Background colors
    background: '#FFFFFF',
    backgroundAlt: '#F8F9FA',
    
    // Status colors
    success: '#10B981', // Green
    error: '#EF4444',   // Red
    warning: '#F59E0B', // Amber
    info: '#3B82F6',    // Blue
    
    // Border colors
    border: '#E5E7EB',
    borderFocus: '#E76F51'
  },
  
  // Typography
  fonts: {
    body: '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif',
    heading: '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif',
  },
  
  // Font sizes
  fontSizes: {
    xs: '0.75rem',
    sm: '0.875rem',
    md: '1rem',
    lg: '1.125rem',
    xl: '1.25rem',
    '2xl': '1.5rem',
    '3xl': '1.875rem',
    '4xl': '2.25rem'
  },
  
  // Font weights
  fontWeights: {
    normal: 400,
    medium: 500,
    semibold: 600,
    bold: 700
  },
  
  // Spacing scale
  space: {
    xs: '0.25rem',
    sm: '0.5rem',
    md: '1rem',
    lg: '1.5rem',
    xl: '2rem',
    '2xl': '3rem',
    '3xl': '4rem'
  },
  
  // Border radius
  radii: {
    sm: '0.25rem',
    md: '0.5rem',
    lg: '0.75rem',
    full: '9999px'
  },
  
  // Shadows
  shadows: {
    sm: '0 1px 2px 0 rgba(0, 0, 0, 0.05)',
    md: '0 4px 10px rgba(0, 0, 0, 0.08)',
    lg: '0 10px 20px rgba(0, 0, 0, 0.1)'
  },
  
  // Animations
  transitions: {
    fast: '150ms ease-in-out',
    normal: '300ms ease-in-out',
    slow: '500ms ease-in-out'
  }
};