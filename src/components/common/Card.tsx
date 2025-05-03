import React from 'react';
import styled from 'styled-components';

interface CardProps {
  children: React.ReactNode;
  title?: string;
  subtitle?: string;
  fullWidth?: boolean;
  footer?: React.ReactNode;
}

const CardContainer = styled.div<{ fullWidth?: boolean }>`
  background-color: ${props => props.theme.colors.background};
  border-radius: ${props => props.theme.radii.lg};
  box-shadow: ${props => props.theme.shadows.md};
  overflow: hidden;
  display: flex;
  flex-direction: column;
  width: ${props => props.fullWidth ? '100%' : '500px'};
  max-width: 100%;
  margin: 0 auto;
`;

const CardHeader = styled.div`
  padding: ${props => props.theme.space.lg} ${props => props.theme.space.lg} ${props => props.theme.space.md};
  border-bottom: 1px solid ${props => props.theme.colors.border};
`;

const CardTitle = styled.h2`
  margin-bottom: ${props => props.theme.space.xs};
  color: ${props => props.theme.colors.text};
  font-size: ${props => props.theme.fontSizes.xl};
`;

const CardSubtitle = styled.p`
  color: ${props => props.theme.colors.textSecondary};
  font-size: ${props => props.theme.fontSizes.md};
  margin-bottom: 0;
`;

const CardContent = styled.div`
  padding: ${props => props.theme.space.lg};
  flex: 1;
`;

const CardFooter = styled.div`
  padding: ${props => props.theme.space.md} ${props => props.theme.space.lg};
  border-top: 1px solid ${props => props.theme.colors.border};
  display: flex;
  justify-content: flex-end;
  gap: ${props => props.theme.space.md};
`;

const Card: React.FC<CardProps> = ({ 
  children, 
  title, 
  subtitle, 
  fullWidth = false,
  footer 
}) => {
  return (
    <CardContainer fullWidth={fullWidth}>
      {(title || subtitle) && (
        <CardHeader>
          {title && <CardTitle>{title}</CardTitle>}
          {subtitle && <CardSubtitle>{subtitle}</CardSubtitle>}
        </CardHeader>
      )}
      <CardContent>{children}</CardContent>
      {footer && <CardFooter>{footer}</CardFooter>}
    </CardContainer>
  );
};

export default Card;