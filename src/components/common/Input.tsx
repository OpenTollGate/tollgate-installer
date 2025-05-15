import React, { forwardRef } from 'react';
import styled, { css } from 'styled-components';

interface InputProps extends React.InputHTMLAttributes<HTMLInputElement> {
  label?: string;
  error?: string;
  fullWidth?: boolean;
  helperText?: string;
}

const InputContainer = styled.div<{ fullWidth?: boolean }>`
  display: flex;
  flex-direction: column;
  margin-bottom: 1rem;
  
  ${props => props.fullWidth && css`
    width: 100%;
  `}
`;

const InputLabel = styled.label`
  font-size: ${props => props.theme.fontSizes.sm};
  font-weight: ${props => props.theme.fontWeights.medium};
  margin-bottom: ${props => props.theme.space.xs};
  color: ${props => props.theme.colors.text};
`;

const StyledInput = styled.input<{ hasError?: boolean }>`
  padding: 0.75rem 1rem;
  font-size: ${props => props.theme.fontSizes.md};
  border: 1px solid ${props => props.hasError ? props.theme.colors.error : props.theme.colors.border};
  border-radius: ${props => props.theme.radii.md};
  transition: all ${props => props.theme.transitions.fast};
  width: 100%;
  background-color: ${props => props.theme.colors.background};
  
  &:focus {
    outline: none;
    border-color: ${props => props.hasError ? props.theme.colors.error : props.theme.colors.primary};
    box-shadow: 0 0 0 2px ${props => props.hasError
      ? `${props.theme.colors.error}33`
      : `${props.theme.colors.primary}33`};
  }
  
  &:hover:not(:disabled):not(:focus) {
    border-color: ${props => props.hasError
      ? props.theme.colors.error
      : props.theme.colors.textSecondary};
  }
  
  &:disabled {
    background-color: ${props => props.theme.colors.backgroundAlt};
    cursor: not-allowed;
  }
  
  &::placeholder {
    color: ${props => props.theme.colors.textSecondary};
    opacity: 0.7;
  }
`;

const ErrorText = styled.span<{ $isSuccess?: boolean }>`
  font-size: ${props => props.theme.fontSizes.sm};
  color: ${props => props.$isSuccess ? props.theme.colors.success : props.theme.colors.error};
  margin-top: ${props => props.theme.space.xs};
`;

const HelperText = styled.span`
  font-size: ${props => props.theme.fontSizes.sm};
  color: ${props => props.theme.colors.textSecondary};
  margin-top: ${props => props.theme.space.xs};
`;

const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ label, error, fullWidth = false, helperText, ...props }, ref) => {
    return (
      <InputContainer fullWidth={fullWidth}>
        {label && <InputLabel>{label}</InputLabel>}
        <StyledInput
          hasError={!!error}
          ref={ref}
          {...props}
        />
        {error && <ErrorText>{error}</ErrorText>}
        {helperText && !error && <HelperText>{helperText}</HelperText>}
      </InputContainer>
    );
  }
);

Input.displayName = 'Input';

export default Input;